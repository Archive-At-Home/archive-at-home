package balance

import (
	"context"
	"time"
)

// ─────────────────────────────────────────────
// Balance / Credit System
//
// Tracks user GP balance, supports deposits (top-up / earned),
// deductions (task consumption), and transaction history.
// ─────────────────────────────────────────────

// Account represents a user's GP balance.
type Account struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    string    `json:"user_id" gorm:"uniqueIndex"`
	Balance   int64     `json:"balance"` // current GP balance (can be negative if deferred billing)
	Frozen    int64     `json:"frozen"`  // GP reserved by in-flight tasks
	UpdatedAt time.Time `json:"updated_at"`
}

// Available returns usable GP (total balance minus frozen).
func (a *Account) Available() int64 {
	return a.Balance - a.Frozen
}

// TransactionType categorises ledger entries.
type TransactionType string

const (
	TxDeposit  TransactionType = "DEPOSIT"  // top-up, admin grant, referral bonus
	TxDeduct   TransactionType = "DEDUCT"   // task GP consumption
	TxRefund   TransactionType = "REFUND"   // failed task refund
	TxFreeze   TransactionType = "FREEZE"   // reserve GP for in-flight task
	TxUnfreeze TransactionType = "UNFREEZE" // release frozen GP
	TxCheckin  TransactionType = "CHECKIN"  // daily checkin reward
)

// Transaction is an immutable ledger entry.
type Transaction struct {
	ID        uint            `json:"id" gorm:"primaryKey"`
	UserID    string          `json:"user_id" gorm:"index"`
	Type      TransactionType `json:"type"`
	Amount    int64           `json:"amount"` // positive = credit, negative = debit
	Balance   int64           `json:"balance_after"`
	TraceID   string          `json:"trace_id,omitempty" gorm:"index"` // link to task
	Remark    string          `json:"remark,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ─────────────────────────────────────────────
// BalanceService defines the interface for the
// GP balance & billing system.
// ─────────────────────────────────────────────

type BalanceService interface {
	// GetAccount returns the user's current balance info.
	// Creates an account with zero balance if not exists.
	GetAccount(ctx context.Context, userID string) (*Account, error)

	// Deposit adds GP to a user's balance.
	Deposit(ctx context.Context, userID string, amount int64, remark string) (*Account, error)

	// FreezeGP reserves GP for an in-flight task.
	// Returns error if insufficient balance.
	FreezeGP(ctx context.Context, userID string, traceID string, amount int64) error

	// SettleTask finalises a completed task:
	//   - Unfreezes the reserved amount
	//   - Deducts the frozen amount (estimatedGP) from balance
	//   - actualGP parameter is for internal statistics only
	//   - Returns the updated account
	SettleTask(ctx context.Context, userID string, traceID string, frozenAmount, actualGP int64) (*Account, error)

	// RefundTask releases frozen GP when a task fails.
	RefundTask(ctx context.Context, userID string, traceID string, frozenAmount int64) (*Account, error)
}
