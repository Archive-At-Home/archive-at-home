package service

import (
	"context"
	"errors"
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

// Service errors
var (
	ErrInsufficientBalance = errors.New("insufficient balance")
)

const errMsgNilResult = "task completed with nil result"
const errMsgTimeout = "timeout waiting for node result"

// GalleryService orchestrates the full request lifecycle:
//
//	publish/collapse (with atomic cache check) → setup created task → wait → return
type GalleryService struct {
	sched        *scheduler.Scheduler
	hub          *ws.Hub
	resultWaiter *ws.ResultWaiter
	claimWaiter  *ws.ClaimWaiter
	store        *store.Store
	cfg          *config.Config
	balanceSvc   balance.BalanceService
}

// NewGalleryService creates the service.
func NewGalleryService(
	sched *scheduler.Scheduler,
	hub *ws.Hub,
	resultWaiter *ws.ResultWaiter,
	claimWaiter *ws.ClaimWaiter,
	store *store.Store,
	cfg *config.Config,
	balanceSvc balance.BalanceService,
) *GalleryService {
	return &GalleryService{
		sched:        sched,
		hub:          hub,
		resultWaiter: resultWaiter,
		claimWaiter:  claimWaiter,
		store:        store,
		cfg:          cfg,
		balanceSvc:   balanceSvc,
	}
}

// setupCreatedTask resolves e-hentai params, freezes balance, and broadcasts.
// Returns (estimatedGP, freeTier, enteredBroadcastFlow, claimedNodeID, error).
// enteredBroadcastFlow=true means the flow reached the broadcast step,
// including send failures and post-broadcast claim failures.
// On error the caller is responsible for cleanup; if enteredBroadcastFlow the task should be logged.
func (s *GalleryService) setupCreatedTask(ctx context.Context, userID string, req *model.ParseRequest, traceID string) (int, bool, bool, string, error) {
	const claimWaitTimeout = 5 * time.Second
	enteredBroadcastFlow := false

	quota, err := ResolveParseParams(ctx, s.cfg, req.GalleryID, req.GalleryKey)
	if err != nil {
		return 0, false, enteredBroadcastFlow, "", fmt.Errorf("resolve e-hentai params: %w", err)
	}

	freeTier := quota.IsNew
	estimatedGP := quota.GP

	if err := s.balanceSvc.FreezeGP(ctx, userID, traceID, int64(estimatedGP)); err != nil {
		if errors.Is(err, balance.ErrInsufficientBalance) {
			return estimatedGP, freeTier, enteredBroadcastFlow, "", ErrInsufficientBalance
		}
		return estimatedGP, freeTier, enteredBroadcastFlow, "", fmt.Errorf("freeze balance: %w", err)
	}
	log.Printf("[service] froze %d GP for user=%s trace=%s", estimatedGP, userID, traceID)

	log.Printf("[service] NEW task trace=%s user=%s gallery=%s key=%s force=%v free=%v estGP=%d",
		traceID, userID, req.GalleryID, req.GalleryKey, req.Force, freeTier, estimatedGP)

	claimCh := s.claimWaiter.Register(traceID)
	defer s.claimWaiter.Unregister(traceID, claimCh)

	// Broadcast announcement to all worker nodes
	enteredBroadcastFlow = true
	err = s.hub.BroadcastTaskAnnouncement(ctx, &model.TaskAnnouncement{
		TraceID:     traceID,
		FreeTier:    freeTier,
		EstimatedGP: estimatedGP,
	})
	if err != nil {
		return estimatedGP, freeTier, enteredBroadcastFlow, "", fmt.Errorf("broadcast announcement: %w", err)
	}

	timer := time.NewTimer(claimWaitTimeout)
	defer timer.Stop()

	select {
	case claimedNodeID := <-claimCh:
		log.Printf("[service] task claimed trace=%s node=%s", traceID, claimedNodeID)
		return estimatedGP, freeTier, enteredBroadcastFlow, claimedNodeID, nil
	case <-timer.C:
		return estimatedGP, freeTier, enteredBroadcastFlow, "", fmt.Errorf("no node claimed task within %s", claimWaitTimeout)
	case <-ctx.Done():
		return estimatedGP, freeTier, enteredBroadcastFlow, "", ctx.Err()
	}
}

// ParseGallery is the main business flow:
//
//  1. Publish/collapse atomically (also checks cache in Lua)
//  2. If created: resolve params + freeze balance + broadcast
//  3. Block (async→sync) until result arrives or timeout
//
// userID is injected by the API key middleware (not from the request body).
func (s *GalleryService) ParseGallery(ctx context.Context, userID string, req *model.ParseRequest) (*model.ParseResponse, error) {
	const compensationTimeout = 5 * time.Second
	baseCtx := context.WithoutCancel(ctx)
	workCtx, workCancel := context.WithTimeout(baseCtx, s.cfg.TaskWaitTimeout)
	defer workCancel()

	// Compensation must not depend on request cancellation, otherwise refund/settle
	// can fail exactly when the request times out or is canceled.
	compensationCtx := func() (context.Context, context.CancelFunc) {
		base := context.WithoutCancel(workCtx)
		return context.WithTimeout(base, compensationTimeout)
	}

	s.store.TouchLastUsed(userID)

	// ── Step 1: Generate request trace and atomically publish/collapse ──
	traceID := uuid.New().String()
	status, payload, err := s.sched.PublishTask(workCtx, traceID, userID, req.GalleryID, req.GalleryKey, req.Force)
	if err != nil {
		return nil, fmt.Errorf("publish task: %w", err)
	}

	if status == scheduler.PublishCached {
		log.Printf("[service] cache HIT user=%s gallery=%s", userID, req.GalleryID)
		return &model.ParseResponse{
			Cached:     true,
			ArchiveURL: payload,
		}, nil
	}

	actualTraceID := payload
	created := status == scheduler.PublishCreated
	resultCh := s.resultWaiter.Register(actualTraceID)
	defer s.resultWaiter.Unregister(actualTraceID, resultCh)

	estimatedGP := 0
	freeTier := false
	claimedNodeID := ""

	// ── Step 2: Setup created task (or log collapsed) ──
	if created {
		var enteredBroadcastFlow bool
		estimatedGP, freeTier, enteredBroadcastFlow, claimedNodeID, err = s.setupCreatedTask(workCtx, userID, req, actualTraceID)
		if err != nil {
			// Fail setup attempts so future requests do not collapse into this trace.
			failCtx, cancel := compensationCtx()
			defer cancel()
			if failErr := s.sched.FailTask(failCtx, actualTraceID, claimedNodeID); failErr != nil {
				log.Printf("[service] fail task error trace=%s node=%s: %v", actualTraceID, claimedNodeID, failErr)
			}
			// Notify any collapsed waiters that the task failed
			s.resultWaiter.Notify(actualTraceID, &model.TaskResult{
				TraceID: actualTraceID,
				Success: false,
				Error:   err.Error(),
			})
			// Refund if balance was frozen (FreezeGP succeeded but later step failed)
			if !errors.Is(err, ErrInsufficientBalance) && estimatedGP > 0 {
				refundCtx, cancel := compensationCtx()
				defer cancel()
				if _, refundErr := s.balanceSvc.RefundTask(refundCtx, userID, actualTraceID, int64(estimatedGP)); refundErr != nil {
					log.Printf("[service] refund balance error for setup failure: %v", refundErr)
				} else {
					log.Printf("[service] refunded %d GP for setup failure trace=%s reason=%v", estimatedGP, actualTraceID, err)
				}
			}
			// Log only if we reached the broadcast step.
			if enteredBroadcastFlow {
				s.store.LogTask(actualTraceID, userID, req.GalleryID, req.GalleryKey,
					req.Force, freeTier, estimatedGP, claimedNodeID, false, err.Error(), 0)
			}
			return &model.ParseResponse{Error: err.Error()}, nil
		}
	} else {
		log.Printf("[service] COLLAPSED into trace=%s user=%s gallery=%s",
			actualTraceID, userID, req.GalleryID)
	}

	// ── Step 3: Wait for result (async → sync bridge) ──
	//
	// Only created tasks that entered broadcast have frozen GP and require DB logging.
	// logTask writes a single DB record; only called from the branches below.
	logTask := func(success bool, failureReason string, actualGP int) {
		if !created {
			return
		}
		s.store.LogTask(actualTraceID, userID, req.GalleryID, req.GalleryKey,
			req.Force, freeTier, estimatedGP, claimedNodeID, success, failureReason, actualGP)
	}

	refund := func(reason string) {
		if !created || estimatedGP == 0 {
			return
		}
		refundCtx, cancel := compensationCtx()
		defer cancel()
		if _, err := s.balanceSvc.RefundTask(refundCtx, userID, actualTraceID, int64(estimatedGP)); err != nil {
			log.Printf("[service] refund balance error %s: %v", reason, err)
			return
		}
		log.Printf("[service] refunded %d GP %s trace=%s", estimatedGP, reason, actualTraceID)
	}

	select {
	case result := <-resultCh:
		if result == nil {
			refund("for nil result")
			logTask(false, errMsgNilResult, 0)
			return &model.ParseResponse{Error: errMsgNilResult}, nil
		}

		failureReason := ""
		if !result.Success {
			failureReason = result.Error
		}
		logTask(result.Success, failureReason, result.ActualGP)

		if created {
			if result.Success {
				settleCtx, cancel := compensationCtx()
				defer cancel()
				if _, err := s.balanceSvc.SettleTask(settleCtx, userID, actualTraceID, int64(estimatedGP), int64(result.ActualGP)); err != nil {
					log.Printf("[service] settle balance error: %v", err)
				} else {
					log.Printf("[service] settled task trace=%s frozen=%d actual=%d", actualTraceID, estimatedGP, result.ActualGP)
				}
			} else {
				refund("for failed task")
			}
		}

		if !result.Success {
			return &model.ParseResponse{Error: result.Error}, nil
		}

		gpCost := estimatedGP
		if !created {
			gpCost = 0 // collapsed request was not charged
		}
		return &model.ParseResponse{
			Cached:     false,
			GPCost:     gpCost,
			ArchiveURL: result.ArchiveURL,
		}, nil

	case <-workCtx.Done():
		refund("for timeout")
		logTask(false, errMsgTimeout, 0)
		if created {
			failCtx, cancel := compensationCtx()
			defer cancel()
			if failErr := s.sched.FailTask(failCtx, actualTraceID, claimedNodeID); failErr != nil {
				log.Printf("[service] fail task error on timeout trace=%s node=%s: %v", actualTraceID, claimedNodeID, failErr)
			}
			// Unblock any collapsed waiters that are still pending.
			s.resultWaiter.Notify(actualTraceID, &model.TaskResult{
				TraceID: actualTraceID,
				Success: false,
				Error:   errMsgTimeout,
			})
		}
		return &model.ParseResponse{Error: errMsgTimeout}, nil
	}
}
