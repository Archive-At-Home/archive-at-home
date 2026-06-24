package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/balance"
	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/scheduler"
	"github.com/Archive-At-Home/archive-at-home/server/internal/store"
	"github.com/Archive-At-Home/archive-at-home/server/internal/ws"
	"github.com/google/uuid"
)

const (
	errMsgTimeout       = "timeout waiting for node result"
	compensationTimeout = 5 * time.Second
)

// GalleryService orchestrates the full request lifecycle:
//
//	publish/collapse → assign → wait → compensate → return
type GalleryService struct {
	sched        *scheduler.Scheduler
	hub          *ws.Hub
	resultWaiter *ws.ResultWaiter
	store        *store.Store
	cfg          *config.Config
	balanceSvc   balance.BalanceService
}

// NewGalleryService creates the service.
func NewGalleryService(
	sched *scheduler.Scheduler,
	hub *ws.Hub,
	resultWaiter *ws.ResultWaiter,
	store *store.Store,
	cfg *config.Config,
	balanceSvc balance.BalanceService,
) *GalleryService {
	return &GalleryService{
		sched:        sched,
		hub:          hub,
		resultWaiter: resultWaiter,
		store:        store,
		cfg:          cfg,
		balanceSvc:   balanceSvc,
	}
}

// ParseGallery is the main business flow.
//
// userID is injected by the API key middleware (not from the request body).
// client identifies the request source (resolved from X-Client header or User-Agent).
func (s *GalleryService) ParseGallery(ctx context.Context, userID, client string, req *model.ParseRequest) (*model.ParseResponse, error) {
	baseCtx := context.WithoutCancel(ctx)
	workCtx, workCancel := context.WithTimeout(baseCtx, s.cfg.TaskWaitTimeout)
	defer workCancel()

	s.store.TouchLastUsed(userID)

	// ── Step 1: Publish (atomic cache check + request collapsing) ──
	traceID := uuid.Must(uuid.NewV7()).String()
	status, payload, err := s.sched.PublishTask(workCtx, traceID, userID, req.GalleryID, req.Force)
	if err != nil {
		return nil, fmt.Errorf("publish task: %w", err)
	}

	if status == scheduler.PublishCached {
		log.Printf("[service] cache HIT user=%s gallery=%s", userID, req.GalleryID)
		return &model.ParseResponse{Cached: true, ArchiveURL: payload}, nil
	}

	actualTraceID := payload
	created := status == scheduler.PublishCreated
	ch := s.resultWaiter.Register(actualTraceID)
	defer s.resultWaiter.Unregister(actualTraceID, ch)

	if !created {
		log.Printf("[service] COLLAPSED into trace=%s user=%s gallery=%s",
			actualTraceID, userID, req.GalleryID)
	}

	// ── Step 2: Setup (creator only) ──
	var (
		estimatedGP int
		freeTier    bool
		candidates  []string
		assignment  *model.TaskAssignment
	)

	if created {
		quota, err := ResolveParseParams(workCtx, s.cfg, req.GalleryID, req.GalleryKey)
		if err != nil {
			s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, 0,
				fmt.Sprintf("resolve e-hentai params: %v", err))
			return &model.ParseResponse{Error: err.Error()}, nil
		}

		freeTier = quota.IsNew
		estimatedGP = quota.GP

		if err := s.balanceSvc.FreezeGP(workCtx, userID, actualTraceID, int64(estimatedGP)); err != nil {
			s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, 0,
				fmt.Sprintf("freeze balance: %v", err))
			return &model.ParseResponse{Error: err.Error()}, nil
		}
		log.Printf("[service] froze %d GP for user=%s trace=%s", estimatedGP, userID, actualTraceID)

		log.Printf("[service] NEW task trace=%s user=%s gallery=%s key=%s force=%v free=%v estGP=%d",
			actualTraceID, userID, req.GalleryID, req.GalleryKey, req.Force, freeTier, estimatedGP)

		candidates = s.hub.SelectCandidates(freeTier, estimatedGP)
		if len(candidates) == 0 {
			s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, estimatedGP,
				"no available nodes")
			return &model.ParseResponse{Error: "no available nodes"}, nil
		}

		assignment = &model.TaskAssignment{
			TraceID:    actualTraceID,
			GalleryID:  req.GalleryID,
			GalleryKey: req.GalleryKey,
		}

		s.hub.AssignTask(candidates[0], assignment)
	}

	// ── Step 3: Wait loop ──
	candidateIdx := 0

	for {
		select {
		case result := <-ch:
			switch {
			case result.Success:
				if created {
					s.compensateSuccess(workCtx, userID, req.GalleryID, actualTraceID, estimatedGP, result.ArchiveURL)
					s.store.LogTask(actualTraceID, userID, client, req.GalleryID, req.GalleryKey,
						req.Force, freeTier, estimatedGP, candidates[candidateIdx], true, "", result.ActualGP)
				}
				return &model.ParseResponse{
					GPCost:     estimatedGP,
					ArchiveURL: result.ArchiveURL,
				}, nil

			case !result.Retriable:
				if created {
					s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, estimatedGP, "")
					s.store.LogTask(actualTraceID, userID, client, req.GalleryID, req.GalleryKey,
						req.Force, freeTier, estimatedGP, candidates[candidateIdx], false, result.Error, result.ActualGP)
				}
				return &model.ParseResponse{Error: result.Error}, nil

			default:
				// Retriable — advance to next candidate.
				if created {
					candidateIdx++
					if candidateIdx >= len(candidates) {
						reason := "all candidates exhausted"
						s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, estimatedGP, reason)
						s.store.LogTask(actualTraceID, userID, client, req.GalleryID, req.GalleryKey,
							req.Force, freeTier, estimatedGP, "", false, reason, result.ActualGP)
						return &model.ParseResponse{Error: reason}, nil
					}
					s.hub.AssignTask(candidates[candidateIdx], assignment)
				}
			}

		case <-workCtx.Done():
			if created {
				s.compensateFailure(workCtx, userID, req.GalleryID, actualTraceID, estimatedGP, errMsgTimeout)
				s.store.LogTask(actualTraceID, userID, client, req.GalleryID, req.GalleryKey,
					req.Force, freeTier, estimatedGP, "", false, errMsgTimeout, 0)
			}
			return &model.ParseResponse{Error: errMsgTimeout}, nil
		}
	}
}

// ─────────────────────────────────────────────
// Compensation
// ─────────────────────────────────────────────

func compensationCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), compensationTimeout)
}

// compensateFailure removes the collapsing sentinel and refunds frozen GP.
// When notifyReason is non-empty, collapsed waiters are notified with that reason.
func (s *GalleryService) compensateFailure(parent context.Context, userID, galleryID, traceID string, estimatedGP int, notifyReason string) {
	ctx, cancel := compensationCtx(parent)
	defer cancel()

	if err := s.sched.FailTask(ctx, userID, galleryID); err != nil {
		log.Printf("[service] fail task error trace=%s: %v", traceID, err)
	}

	if estimatedGP > 0 {
		if _, err := s.balanceSvc.RefundTask(ctx, userID, traceID, int64(estimatedGP)); err != nil {
			log.Printf("[service] refund error: %v", err)
		} else {
			log.Printf("[service] refunded %d GP trace=%s", estimatedGP, traceID)
		}
	}

	if notifyReason != "" {
		s.resultWaiter.Notify(traceID, &model.TaskResult{
			TraceID: traceID,
			Success: false,
			Error:   notifyReason,
		})
	}
}

// compensateSuccess stores the result in cache, removes the sentinel, and settles the balance.
func (s *GalleryService) compensateSuccess(parent context.Context, userID, galleryID, traceID string, estimatedGP int, archiveURL string) {
	ctx, cancel := compensationCtx(parent)
	defer cancel()

	if err := s.sched.CompleteTask(ctx, userID, galleryID, archiveURL); err != nil {
		log.Printf("[service] complete task error trace=%s: %v", traceID, err)
	}

	if _, err := s.balanceSvc.SettleTask(ctx, userID, traceID, int64(estimatedGP), 0); err != nil {
		log.Printf("[service] settle balance error: %v", err)
	} else {
		log.Printf("[service] settled task trace=%s frozen=%d", traceID, estimatedGP)
	}
}
