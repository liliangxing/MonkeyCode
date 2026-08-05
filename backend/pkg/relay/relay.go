// Package relay 账号接力管理器
//
// 当一个账号的模型额度/token即将耗尽时，自动切换到下一个账号，
// 并将当前上下文和未完成任务信息传递给新账号继续执行。
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RelayManager 账号接力管理器
type RelayManager struct {
	redis  *redis.Client
	logger *slog.Logger
	cfg    *Config
	mu     sync.RWMutex
	// exhaustedAccounts 已耗尽额度的账号集合 (accountKey -> 耗尽时间)
	exhaustedAccounts map[string]time.Time
}

// Config 接力配置
type Config struct {
	// Enabled 是否启用账号接力
	Enabled bool `mapstructure:"enabled"`
	// TokenWarningThreshold token 预警阈值 (百分比，如 80 表示 80%)
	TokenWarningThreshold int `mapstructure:"token_warning_threshold"`
	// ContextTTL 上下文保存时间 (秒)
	ContextTTL int `mapstructure:"context_ttl"`
	// MaxRelayCount 最大接力次数
	MaxRelayCount int `mapstructure:"max_relay_count"`
	// CooldownSeconds 额度耗尽后账号冷却时间 (秒)
	CooldownSeconds int `mapstructure:"cooldown_seconds"`
}

// DefaultConfig 默认接力配置
func DefaultConfig() Config {
	return Config{
		Enabled:               true,
		TokenWarningThreshold: 80,
		ContextTTL:            3600,
		MaxRelayCount:         5,
		CooldownSeconds:       3600,
	}
}

// NewRelayManager 创建账号接力管理器
func NewRelayManager(redis *redis.Client, logger *slog.Logger, cfg Config) *RelayManager {
	return &RelayManager{
		redis:             redis,
		logger:            logger.With("module", "relay.manager"),
		cfg:               &cfg,
		exhaustedAccounts: make(map[string]time.Time),
	}
}

// AccountInfo 账号信息 (用于接力)
type AccountInfo struct {
	Email    string `json:"email"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	// Index 在账号池中的序号
	Index int `json:"index"`
}

// ContextSnapshot 上下文快照 (接力时传递的关键信息)
type ContextSnapshot struct {
	TaskID          uuid.UUID `json:"task_id"`
	OriginalPrompt  string    `json:"original_prompt"`
	// Summary 当前任务进度摘要 (由 agent 生成或提取)
	Summary string `json:"summary"`
	// UnfinishedTasks 未完成的子任务列表
	UnfinishedTasks []string `json:"unfinished_tasks,omitempty"`
	// KeyDecisions 关键决策/约束
	KeyDecisions []string `json:"key_decisions,omitempty"`
	// FileChanges 文件变更摘要
	FileChanges []string `json:"file_changes,omitempty"`
	// PreviousAccount 上一个账号信息
	PreviousAccount string `json:"previous_account,omitempty"`
	// RelayCount 接力次数
	RelayCount int `json:"relay_count"`
	// CreatedAt 快照时间
	CreatedAt int64 `json:"created_at"`
}

// QuotaError 表示额度/限流错误
type QuotaError struct {
	AccountKey string
	ErrorType  string // "rate_limit", "quota_exceeded", "insufficient_quota", "billing"
	Message    string
}

// 额度耗尽错误关键词
var quotaErrorPatterns = []string{
	"rate_limit",
	"rate limit",
	"429",
	"quota_exceeded",
	"quota exceeded",
	"insufficient_quota",
	"insufficient quota",
	"billing",
	"payment required",
	"credit",
	"balance",
	"exceeded your current quota",
	"limit reached",
	"too many requests",
	"account deactivated",
}

// IsQuotaExhausted 判断错误信息是否表示额度耗尽
func IsQuotaExhausted(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, pattern := range quotaErrorPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// MarkAccountExhausted 标记账号额度已耗尽
func (r *RelayManager) MarkAccountExhausted(ctx context.Context, accountKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.exhaustedAccounts[accountKey] = time.Now()

	// 同时写入 Redis (跨实例共享)
	key := fmt.Sprintf("relay:exhausted:%s", accountKey)
	r.redis.Set(ctx, key, time.Now().Unix(), time.Duration(r.cfg.CooldownSeconds)*time.Second)

	r.logger.WarnContext(ctx, "account marked as exhausted",
		"account", accountKey,
		"cooldown_seconds", r.cfg.CooldownSeconds,
	)
}

// IsAccountExhausted 检查账号是否已耗尽 (且仍在冷却期)
func (r *RelayManager) IsAccountExhausted(ctx context.Context, accountKey string) bool {
	// 先检查本地缓存
	r.mu.RLock()
	exhaustedAt, ok := r.exhaustedAccounts[accountKey]
	r.mu.RUnlock()

	if ok {
		if time.Since(exhaustedAt) < time.Duration(r.cfg.CooldownSeconds)*time.Second {
			return true
		}
		// 冷却期已过，清理
		r.mu.Lock()
		delete(r.exhaustedAccounts, accountKey)
		r.mu.Unlock()
	}

	// 再检查 Redis
	key := fmt.Sprintf("relay:exhausted:%s", accountKey)
	_, err := r.redis.Get(ctx, key).Result()
	return err == nil
}

// PickNextAccount 从账号池中选择下一个可用账号
//
// accounts: 按优先级排序的账号列表
// currentAccountKey: 当前使用的账号 (将被跳过)
func (r *RelayManager) PickNextAccount(ctx context.Context, accounts []*AccountInfo, currentAccountKey string) (*AccountInfo, error) {
	for _, acc := range accounts {
		key := acc.AccountKey()
		if key == currentAccountKey {
			continue
		}
		if !r.IsAccountExhausted(ctx, key) {
			r.logger.InfoContext(ctx, "picked next account for relay",
				"account", acc.Email,
				"index", acc.Index,
			)
			return acc, nil
		}
	}
	return nil, fmt.Errorf("no available account for relay, all accounts exhausted")
}

// SaveContext 保存上下文快照到 Redis
func (r *RelayManager) SaveContext(ctx context.Context, snapshot *ContextSnapshot) error {
	key := fmt.Sprintf("relay:context:%s", snapshot.TaskID)
	b, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal context snapshot: %w", err)
	}
	ttl := time.Duration(r.cfg.ContextTTL) * time.Second
	if err := r.redis.Set(ctx, key, string(b), ttl).Err(); err != nil {
		return fmt.Errorf("save context to redis: %w", err)
	}

	r.logger.InfoContext(ctx, "context snapshot saved",
		"task_id", snapshot.TaskID,
		"relay_count", snapshot.RelayCount,
		"summary_len", len(snapshot.Summary),
		"unfinished_tasks", len(snapshot.UnfinishedTasks),
	)
	return nil
}

// LoadContext 从 Redis 加载上下文快照
func (r *RelayManager) LoadContext(ctx context.Context, taskID uuid.UUID) (*ContextSnapshot, error) {
	key := fmt.Sprintf("relay:context:%s", taskID)
	b, err := r.redis.Get(ctx, key).Bytes()
	if err != nil {
		return nil, fmt.Errorf("load context from redis: %w", err)
	}
	var snapshot ContextSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal context snapshot: %w", err)
	}
	return &snapshot, nil
}

// ClearContext 清除上下文快照
func (r *RelayManager) ClearContext(ctx context.Context, taskID uuid.UUID) error {
	key := fmt.Sprintf("relay:context:%s", taskID)
	return r.redis.Del(ctx, key).Err()
}

// BuildRelayPrompt 构建接力提示词 (注入到新账号的系统提示中)
func BuildRelayPrompt(snapshot *ContextSnapshot) string {
	var sb strings.Builder

	sb.WriteString("\n\n=== 账号接力上下文 ===\n")
	sb.WriteString("你正在接替前一个账号继续执行任务。以下是关键上下文信息：\n\n")

	if snapshot.OriginalPrompt != "" {
		sb.WriteString("## 原始任务描述\n")
		sb.WriteString(snapshot.OriginalPrompt)
		sb.WriteString("\n\n")
	}

	if snapshot.Summary != "" {
		sb.WriteString("## 当前进度摘要\n")
		sb.WriteString(snapshot.Summary)
		sb.WriteString("\n\n")
	}

	if len(snapshot.UnfinishedTasks) > 0 {
		sb.WriteString("## 未完成的子任务\n")
		for i, task := range snapshot.UnfinishedTasks {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, task))
		}
		sb.WriteString("\n")
	}

	if len(snapshot.KeyDecisions) > 0 {
		sb.WriteString("## 关键决策与约束\n")
		for _, d := range snapshot.KeyDecisions {
			sb.WriteString(fmt.Sprintf("- %s\n", d))
		}
		sb.WriteString("\n")
	}

	if len(snapshot.FileChanges) > 0 {
		sb.WriteString("## 已变更的文件\n")
		for _, f := range snapshot.FileChanges {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("请基于以上上下文继续执行任务，保持与之前一致的风格和方向。\n")
	sb.WriteString("=== 接力上下文结束 ===\n")

	return sb.String()
}

// AccountKey 生成账号唯一标识
func (a *AccountInfo) AccountKey() string {
	return fmt.Sprintf("%s:%s", a.Provider, a.Email)
}

// ShouldRelay 判断是否应该进行接力
func (r *RelayManager) ShouldRelay(ctx context.Context, taskID uuid.UUID, relayCount int) bool {
	if !r.cfg.Enabled {
		return false
	}
	if relayCount >= r.cfg.MaxRelayCount {
		r.logger.WarnContext(ctx, "max relay count reached, stopping relay",
			"task_id", taskID,
			"relay_count", relayCount,
			"max", r.cfg.MaxRelayCount,
		)
		return false
	}
	return true
}

// GetRelayCount 获取当前任务的接力次数
func (r *RelayManager) GetRelayCount(ctx context.Context, taskID uuid.UUID) (int, error) {
	key := fmt.Sprintf("relay:count:%s", taskID)
	count, err := r.redis.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	// 设置过期时间
	r.redis.Expire(ctx, key, time.Duration(r.cfg.ContextTTL)*time.Second)
	return int(count), nil
}

// SaveLastInput 保存最近一次用户输入 (用于接力时恢复)
func (r *RelayManager) SaveLastInput(ctx context.Context, taskID uuid.UUID, content string) error {
	key := fmt.Sprintf("relay:last_input:%s", taskID)
	return r.redis.Set(ctx, key, content, 24*time.Hour).Err()
}

// LoadLastInput 加载最近一次用户输入
func (r *RelayManager) LoadLastInput(ctx context.Context, taskID uuid.UUID) (string, error) {
	key := fmt.Sprintf("relay:last_input:%s", taskID)
	content, err := r.redis.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}
	return content, nil
}
