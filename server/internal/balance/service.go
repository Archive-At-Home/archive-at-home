package balance

import (
	"context"
	"errors"
	"log"
	"time"

	"gorm.io/gorm"
)

// ─────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────

var (
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrAccountNotFound     = errors.New("account not found")
)

// ─────────────────────────────────────────────
// balanceService implements BalanceService
// ─────────────────────────────────────────────

type balanceService struct {
	db *gorm.DB
}

// NewBalanceService creates a new BalanceService backed by the given DB.
func NewBalanceService(db *gorm.DB) BalanceService {
	return &balanceService{db: db}
}

// GetAccount returns the user's balance account, creating one if not exists.
func (s *balanceService) GetAccount(ctx context.Context, userID string) (*Account, error) {
	var acc Account
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&acc).Error
	if err == nil {
		return &acc, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Create new account with zero balance
	acc = Account{
		UserID:    userID,
		Balance:   0,
		Frozen:    0,
		UpdatedAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(&acc).Error; err != nil {
		// Handle race condition: another goroutine might have created it
		if err2 := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&acc).Error; err2 == nil {
			return &acc, nil
		}
		return nil, err
	}
	return &acc, nil
}

// Deposit adds GP to a user's balance.
func (s *balanceService) Deposit(ctx context.Context, userID string, amount int64, remark string) (*Account, error) {
	return s.withTx(ctx, func(tx *gorm.DB) (*Account, error) {
		acc, err := s.getOrCreateAccountTx(tx, userID)
		if err != nil {
			return nil, err
		}

		acc.Balance += amount
		acc.UpdatedAt = time.Now()

		if err := tx.Save(acc).Error; err != nil {
			return nil, err
		}

		// Record transaction
		txn := Transaction{
			UserID:    userID,
			Type:      TxDeposit,
			Amount:    amount,
			Balance:   acc.Balance,
			Remark:    remark,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&txn).Error; err != nil {
			return nil, err
		}

		return acc, nil
	})
}

// FreezeGP reserves GP for an in-flight task.
func (s *balanceService) FreezeGP(ctx context.Context, userID string, traceID string, amount int64) error {
	_, err := s.withTx(ctx, func(tx *gorm.DB) (*Account, error) {
		acc, err := s.getOrCreateAccountTx(tx, userID)
		if err != nil {
			return nil, err
		}

		if acc.Available() < amount {
			return nil, ErrInsufficientBalance
		}

		acc.Frozen += amount
		acc.UpdatedAt = time.Now()

		if err := tx.Save(acc).Error; err != nil {
			return nil, err
		}

		// Record freeze transaction
		txn := Transaction{
			UserID:    userID,
			Type:      TxFreeze,
			Amount:    -amount, // Frozen is a debit from available
			Balance:   acc.Balance,
			TraceID:   traceID,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&txn).Error; err != nil {
			return nil, err
		}

		return acc, nil
	})
	return err
}

// SettleTask finalises a completed task.
func (s *balanceService) SettleTask(ctx context.Context, userID string, traceID string, frozenAmount, actualGP int64) (*Account, error) {
	return s.withTx(ctx, func(tx *gorm.DB) (*Account, error) {
		var acc Account
		if err := tx.Where("user_id = ?", userID).First(&acc).Error; err != nil {
			return nil, err
		}

		// Unfreeze the reserved amount
		acc.Frozen -= frozenAmount
		if acc.Frozen < 0 {
			log.Printf("[balance] WARNING: frozen went negative for user=%s trace=%s (frozen=%d amount=%d), resetting to 0",
				userID, traceID, acc.Frozen+frozenAmount, frozenAmount)
			acc.Frozen = 0
		}

		// Record unfreeze
		txnUnfreeze := Transaction{
			UserID:    userID,
			Type:      TxUnfreeze,
			Amount:    frozenAmount,
			Balance:   acc.Balance,
			TraceID:   traceID,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&txnUnfreeze).Error; err != nil {
			return nil, err
		}

		// Deduct estimated GP (always charge based on frozen amount)
		// actualGP is for internal statistics only
		acc.Balance -= frozenAmount

		txnDeduct := Transaction{
			UserID:    userID,
			Type:      TxDeduct,
			Amount:    -frozenAmount,
			Balance:   acc.Balance,
			TraceID:   traceID,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&txnDeduct).Error; err != nil {
			return nil, err
		}

		acc.UpdatedAt = time.Now()
		if err := tx.Save(&acc).Error; err != nil {
			return nil, err
		}

		return &acc, nil
	})
}

// RefundTask releases frozen GP when a task fails.
func (s *balanceService) RefundTask(ctx context.Context, userID string, traceID string, frozenAmount int64) (*Account, error) {
	return s.withTx(ctx, func(tx *gorm.DB) (*Account, error) {
		var acc Account
		if err := tx.Where("user_id = ?", userID).First(&acc).Error; err != nil {
			return nil, err
		}

		// Unfreeze the reserved amount
		acc.Frozen -= frozenAmount
		if acc.Frozen < 0 {
			log.Printf("[balance] WARNING: frozen went negative for user=%s trace=%s (frozen=%d amount=%d), resetting to 0",
				userID, traceID, acc.Frozen+frozenAmount, frozenAmount)
			acc.Frozen = 0
		}
		acc.UpdatedAt = time.Now()

		if err := tx.Save(&acc).Error; err != nil {
			return nil, err
		}

		// Record refund transaction
		txn := Transaction{
			UserID:    userID,
			Type:      TxRefund,
			Amount:    frozenAmount,
			Balance:   acc.Balance,
			TraceID:   traceID,
			Remark:    "task failed/cancelled",
			CreatedAt: time.Now(),
		}
		if err := tx.Create(&txn).Error; err != nil {
			return nil, err
		}

		return &acc, nil
	})
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

func (s *balanceService) withTx(ctx context.Context, fn func(tx *gorm.DB) (*Account, error)) (*Account, error) {
	var result *Account
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		acc, err := fn(tx)
		if err != nil {
			return err
		}
		result = acc
		return nil
	})
	return result, err
}

func (s *balanceService) getOrCreateAccountTx(tx *gorm.DB, userID string) (*Account, error) {
	var acc Account
	err := tx.Where("user_id = ?", userID).First(&acc).Error
	if err == nil {
		return &acc, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	acc = Account{
		UserID:    userID,
		Balance:   0,
		Frozen:    0,
		UpdatedAt: time.Now(),
	}
	if err := tx.Create(&acc).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}
