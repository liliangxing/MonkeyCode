package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/ent/types"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/cvt"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
	"github.com/chaitin/MonkeyCode/backend/pkg/lifecycle"
	"github.com/chaitin/MonkeyCode/backend/pkg/loki"
	"github.com/chaitin/MonkeyCode/backend/pkg/notify/dispatcher"
	"github.com/chaitin/MonkeyCode/backend/pkg/relay"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/chaitin/MonkeyCode/backend/templates"
)

// TaskUsecase 任务业务逻辑实现
type TaskUsecase struct {
	cfg              *config.Config
	repo             domain.TaskRepo
	modelRepo        domain.ModelRepo
	logger           *slog.Logger
	taskflow         taskflow.Clienter
	loki             *loki.Client
	redis            *redis.Client
	notifyDispatcher *dispatcher.Dispatcher
	taskHook         domain.TaskHook
	taskLifecycle    *lifecycle.Manager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata]
	vmLifecycle      *lifecycle.Manager[string, lifecycle.VMState, lifecycle.VMMetadata]
	relayManager     *relay.RelayManager
}

// NewTaskUsecase 创建任务业务逻辑实例
func NewTaskUsecase(i *do.Injector) (domain.TaskUsecase, error) {
	u := &TaskUsecase{
		cfg:              do.MustInvoke[*config.Config](i),
		repo:             do.MustInvoke[domain.TaskRepo](i),
		modelRepo:        do.MustInvoke[domain.ModelRepo](i),
		logger:           do.MustInvoke[*slog.Logger](i).With("module", "usecase.TaskUsecase"),
		taskflow:         do.MustInvoke[taskflow.Clienter](i),
		loki:             do.MustInvoke[*loki.Client](i),
		redis:            do.MustInvoke[*redis.Client](i),
		notifyDispatcher: do.MustInvoke[*dispatcher.Dispatcher](i),
		taskLifecycle:    do.MustInvoke[*lifecycle.Manager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata]](i),
		vmLifecycle:      do.MustInvoke[*lifecycle.Manager[string, lifecycle.VMState, lifecycle.VMMetadata]](i),
	}

	// 可选注入 TaskHook
	if hook, err := do.Invoke[domain.TaskHook](i); err == nil {
		u.taskHook = hook
	}

	// 初始化账号接力管理器
	relayCfg := relay.DefaultConfig()
	if u.cfg.Relay.Enabled || u.cfg.Relay.MaxRelayCount > 0 {
		relayCfg = relay.Config{
			Enabled:               u.cfg.Relay.Enabled,
			TokenWarningThreshold: u.cfg.Relay.TokenWarningThreshold,
			ContextTTL:            u.cfg.Relay.ContextTTL,
			MaxRelayCount:         u.cfg.Relay.MaxRelayCount,
			CooldownSeconds:       u.cfg.Relay.CooldownSeconds,
		}
	}
	u.relayManager = relay.NewRelayManager(u.redis, u.logger, relayCfg)

	return u, nil
}

// AutoApprove implements domain.TaskUsecase.
func (a *TaskUsecase) AutoApprove(ctx context.Context, _ *domain.User, id uuid.UUID, approve bool) error {
	return a.taskflow.TaskManager().AutoApprove(ctx, taskflow.TaskApproveReq{
		ID:          id,
		AutoApprove: &approve,
	})
}

// Info implements domain.TaskUsecase.
func (a *TaskUsecase) Info(ctx context.Context, user *domain.User, id uuid.UUID) (*domain.Task, bool, error) {
	ctx = entx.SkipSoftDelete(ctx)

	t, err := a.repo.Info(ctx, user, id)
	if err != nil {
		return nil, false, err
	}

	owner := user.ID == t.UserID

	tk := cvt.From(t, &domain.Task{})
	if vm := tk.VirtualMachine; vm != nil {
		resp, _ := a.taskflow.VirtualMachiner().IsOnline(ctx, &taskflow.IsOnlineReq[string]{
			IDs: []string{vm.ID},
		})

		if resp != nil && resp.OnlineMap[vm.ID] {
			vm.Status = taskflow.VirtualMachineStatusOnline
		} else {
			vm.Status = taskflow.VirtualMachineStatusPending

			for _, cond := range vm.Conditions {
				switch cond.Type {
				case types.ConditionTypeFailed:
					vm.Status = taskflow.VirtualMachineStatusOffline
				case types.ConditionTypeReady:
					if time.Since(time.Unix(vm.CreatedAt, 0)) > 2*time.Minute {
						vm.Status = taskflow.VirtualMachineStatusOffline
					}
				}
			}
		}

		// 端口信息
		ports, _ := a.taskflow.PortForwarder().List(ctx, vm.ID)
		if ports == nil {
			ports = []*taskflow.PortForwardInfo{}
		}
		vmPorts := cvt.Iter(ports, func(_ int, port *taskflow.PortForwardInfo) *domain.VMPort {
			return &domain.VMPort{
				ForwardID:    port.ForwardID,
				Port:         uint16(port.Port),
				Status:       consts.PortStatus(port.Status),
				WhiteList:    port.WhitelistIPs,
				ErrorMessage: port.ErrorMessage,
				PreviewURL:   port.AccessURL,
			}
		})
		sort.Slice(vmPorts, func(i, j int) bool {
			return vmPorts[i].Port < vmPorts[j].Port
		})
		vm.Ports = vmPorts
	}

	if stat, err := a.repo.Stat(ctx, id); err == nil {
		tk.Stats = stat
	}

	return tk, owner, nil
}

// List implements domain.TaskUsecase.
func (a *TaskUsecase) List(ctx context.Context, user *domain.User, req domain.TaskListReq) (*domain.ListTaskResp, error) {
	ctx = entx.SkipSoftDelete(ctx)
	projectTasks, pageInfo, err := a.repo.List(ctx, user, req)
	if err != nil {
		return nil, err
	}

	stat, err := a.repo.StatByIDs(ctx, cvt.Iter(projectTasks, func(_ int, pt *db.ProjectTask) uuid.UUID {
		return pt.TaskID
	}))
	if err != nil {
		return nil, err
	}

	tasks := cvt.Iter(projectTasks, func(_ int, pt *db.ProjectTask) *domain.ProjectTask {
		tmp := cvt.From(pt, &domain.ProjectTask{})
		if tmp.Task != nil {
			tmp.Task.Stats = stat[tmp.Task.ID]
		}
		return tmp
	})

	resp := &domain.ListTaskResp{Tasks: tasks}
	if pageInfo != nil {
		resp.PageInfo = pageInfo
	}
	return resp, nil
}

// Stop implements domain.TaskUsecase.
func (a *TaskUsecase) Stop(ctx context.Context, user *domain.User, id uuid.UUID) error {
	t, err := a.repo.Info(ctx, user, id)
	if err != nil {
		return err
	}
	tk := cvt.From(t, &domain.Task{})
	return a.repo.Stop(ctx, user, id, func(t *db.Task) error {
		return a.taskflow.TaskManager().Stop(ctx, taskflow.TaskReq{
			VirtualMachine: &taskflow.VirtualMachine{
				ID:            tk.VirtualMachine.ID,
				HostID:        tk.VirtualMachine.Host.ID,
				EnvironmentID: tk.VirtualMachine.EnvironmentID,
			},
			Task: &taskflow.Task{
				ID: id,
			},
		})
	})
}

// Cancel implements domain.TaskUsecase.
func (a *TaskUsecase) Cancel(ctx context.Context, user *domain.User, id uuid.UUID) error {
	t, err := a.repo.Info(ctx, user, id)
	if err != nil {
		return err
	}
	tk := cvt.From(t, &domain.Task{})

	if err := a.taskflow.TaskManager().Cancel(ctx, taskflow.TaskReq{
		VirtualMachine: &taskflow.VirtualMachine{
			ID:            tk.VirtualMachine.ID,
			HostID:        tk.VirtualMachine.Host.ID,
			EnvironmentID: tk.VirtualMachine.EnvironmentID,
		},
		Task: &taskflow.Task{
			ID: id,
		},
	}); err != nil {
		return err
	}

	return nil
}

// Continue implements domain.TaskUsecase.
func (a *TaskUsecase) Continue(ctx context.Context, user *domain.User, id uuid.UUID, content string) error {
	t, err := a.repo.Info(ctx, user, id)
	if err != nil {
		return err
	}
	tk := cvt.From(t, &domain.Task{})
	if err := a.taskflow.TaskManager().Continue(ctx, taskflow.TaskReq{
		VirtualMachine: &taskflow.VirtualMachine{
			ID:            tk.VirtualMachine.ID,
			HostID:        tk.VirtualMachine.Host.ID,
			EnvironmentID: tk.VirtualMachine.EnvironmentID,
		},
		Task: &taskflow.Task{
			ID:   id,
			Text: content,
		},
	}); err != nil {
		return err
	}

	// 缓存最近一次 user-input，供通知推送使用
	a.redis.Set(ctx, fmt.Sprintf("mcai:task:%s:last_input", id.String()), content, 24*time.Hour)

	// 账号接力: 同时缓存到 relay manager (供接力时恢复上下文)
	if a.relayManager != nil {
		if err := a.relayManager.SaveLastInput(ctx, id, content); err != nil {
			a.logger.WarnContext(ctx, "failed to save last input for relay", "error", err)
		}
	}

	return nil
}

// Create implements domain.TaskUsecase.
func (a *TaskUsecase) Create(ctx context.Context, user *domain.User, req domain.CreateTaskReq, token string) (*domain.ProjectTask, error) {
	r, err := a.taskflow.Host().IsOnline(ctx, &taskflow.IsOnlineReq[string]{
		IDs: []string{req.HostID},
	})
	if err != nil {
		return nil, errcode.ErrHostOffline.Wrap(err)
	}
	if !r.OnlineMap[req.HostID] {
		return nil, errcode.ErrHostOffline
	}

	TTLType := taskflow.TTLCountDown
	if req.Resource.Life <= 0 {
		TTLType = taskflow.TTLForever
	}

	req.Now = time.Now()

	// 如果有 TaskHook，获取系统提示词
	if a.taskHook != nil && req.SystemPrompt == "" {
		if prompt, err := a.taskHook.GetSystemPrompt(ctx, req.Type, req.SubType); err == nil && prompt != "" {
			req.SystemPrompt = prompt
		}
	}

	pt, err := a.repo.Create(ctx, user, req, token, func(pt *db.ProjectTask, m *db.Model, i *db.Image) (*taskflow.VirtualMachine, error) {
		t := pt.Edges.Task
		if t == nil {
			return nil, fmt.Errorf("task edge is nil")
		}

		coding, configs, err := a.getCodingConfigs(req.CliName, m, req.Extra.SkillIDs)
		if err != nil {
			return nil, err
		}

		git := taskflow.Git{
			URL:    pt.RepoURL,
			Branch: pt.Branch,
		}
		if token != "" {
			git.Token = token
		}

		vm, err := a.taskflow.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{
			UserID:   user.ID.String(),
			HostID:   req.HostID,
			HostName: t.ID.String(),
			Git:      git,
			ZipUrl:   req.RepoReq.ZipURL,
			ImageURL: i.Name,
			ProxyURL: "",
			TTL: taskflow.TTL{
				Kind:    TTLType,
				Seconds: req.Resource.Life,
			},
			TaskID: t.ID,
			LLM: taskflow.LLMProviderReq{
				Provider: taskflow.LlmProviderOpenAI,
				ApiKey:   m.APIKey,
				BaseURL:  m.BaseURL,
				Model:    m.Model,
			},
			Cores:  fmt.Sprintf("%d", req.Resource.Core),
			Memory: req.Resource.Memory,
		})
		if err != nil {
			return nil, err
		}

		if vm == nil {
			return nil, fmt.Errorf("vm is nil")
		}

		mcps := []taskflow.McpServerConfig{
			{
				Type: "http",
				Name: "mcaiBuiltin",
				Url:  proto.String(fmt.Sprintf("http://127.0.0.1:65510/mcp?task_id=%s", t.ID.String())),
			},
			{
				Type: "http",
				Name: "context7",
				Url:  proto.String("https://mcp.context7.com/mcp"),
				Headers: []*taskflow.McpHttpHeader{
					{
						Name:  "CONTEXT7_API_KEY",
						Value: a.cfg.Context7ApiKey,
					},
				},
			},
		}

		taskMeta := lifecycle.TaskMetadata{
			TaskID: t.ID,
			UserID: user.ID,
		}
		if err := a.taskLifecycle.Transition(ctx, t.ID, consts.TaskStatusPending, taskMeta); err != nil {
			a.logger.WarnContext(ctx, "task lifecycle transition failed", "error", err)
		}

		vmMeta := lifecycle.VMMetadata{
			VMID:   vm.ID,
			TaskID: &t.ID,
			UserID: user.ID,
		}
		if err := a.vmLifecycle.Transition(ctx, vm.ID, lifecycle.VMStatePending, vmMeta); err != nil {
			a.logger.WarnContext(ctx, "vm lifecycle transition failed", "error", err)
		}

		// 存储 CreateTaskReq 到 Redis（10 分钟过期），供 Lifecycle Manager 消费
		createTaskReq := &taskflow.CreateTaskReq{
			ID:           t.ID,
			VMID:         vm.ID,
			Text:         req.Content,
			SystemPrompt: req.SystemPrompt,
			CodingAgent:  coding,
			LLM: taskflow.LLM{
				ApiKey:  m.APIKey,
				BaseURL: m.BaseURL,
				Model:   m.Model,
			},
			Configs:    configs,
			McpConfigs: mcps,
		}

		// 账号接力: 收集所有可用的模型配置作为备用 LLM 列表
		if a.relayManager != nil {
			llmList := a.buildLLMList(ctx, user.ID, m)
			if len(llmList) > 0 {
				createTaskReq.LLMList = llmList
				a.logger.InfoContext(ctx, "relay LLM list prepared",
					"task_id", t.ID,
					"fallback_count", len(llmList),
				)
			}
		}

		b, err := json.Marshal(createTaskReq)
		if err != nil {
			return vm, err
		}
		reqKey := fmt.Sprintf("task:create_req:%s", t.ID.String())
		if err := a.redis.Set(ctx, reqKey, string(b), 10*time.Minute).Err(); err != nil {
			a.logger.WarnContext(ctx, "failed to store CreateTaskReq in Redis", "error", err)
		}

		return vm, nil
	})
	if err != nil {
		a.logger.With("error", err, "req", req).ErrorContext(ctx, "failed to create task")
		return nil, err
	}
	a.logger.With("req", req).InfoContext(ctx, "task created")

	result := cvt.From(pt, &domain.ProjectTask{})

	// 通知 TaskHook（如内部项目的 git task 创建等）
	if a.taskHook != nil {
		if err := a.taskHook.OnTaskCreated(ctx, result); err != nil {
			a.logger.WarnContext(ctx, "taskHook.OnTaskCreated failed", "error", err)
		}
	}

	return result, nil
}

func (a *TaskUsecase) getCodingConfigs(cli consts.CliName, m *db.Model, skillIDs []string) (taskflow.CodingAgent, []taskflow.ConfigFile, error) {
	var tmp string
	var path string
	var coding taskflow.CodingAgent
	cfs := make([]taskflow.ConfigFile, 0)
	switch cli {
	case consts.CliNameClaude:
		tmp = string(templates.Claude)
		path = "~/.claude/settings.json"
		coding = taskflow.CodingAgentClaude
		m.BaseURL = strings.ReplaceAll(m.BaseURL, "/v1", "")

	case consts.CliNameCodex:
		tmp = string(templates.Codex)
		path = "~/.codex/config.toml"
		coding = taskflow.CodingAgentCodex

	case consts.CliNameOpencode:
		tmp = string(templates.OpenCode)
		path = "~/.config/opencode/opencode.json"
		coding = taskflow.CodingAgentOpenCode

		authtemp, err := template.New("auth").Parse(string(templates.OpenCodeAuth))
		if err != nil {
			return coding, nil, err
		}

		var authBuf bytes.Buffer
		if err := authtemp.Execute(&authBuf, map[string]any{
			"api_key": m.APIKey,
		}); err != nil {
			return coding, nil, err
		}
		cfs = append(cfs, taskflow.ConfigFile{
			Path:    "~/.local/share/opencode/auth.json",
			Content: authBuf.String(),
		})

	default:
		return coding, nil, fmt.Errorf("unexpected consts.CliName: %#v", cli)
	}

	temp, err := template.New("config").Parse(tmp)
	if err != nil {
		return coding, nil, err
	}

	var buf bytes.Buffer
	if err := temp.Execute(&buf, map[string]any{
		"model":    m.Model,
		"base_url": m.BaseURL,
		"api_key":  m.APIKey,
	}); err != nil {
		return coding, nil, err
	}

	cfs = append(cfs, taskflow.ConfigFile{
		Path:    path,
		Content: buf.String(),
	})

	if len(skillIDs) == 0 {
		return coding, cfs, nil
	}

	for _, skillID := range skillIDs {
		skilldir := filepath.Join(consts.SkillBaseDir, skillID)
		if _, err := os.Stat(skilldir); os.IsNotExist(err) {
			continue
		}
		filepath.Walk(skilldir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// 获取相对于 skilldir 的相对路径，保留目录结构
			relPath, err := filepath.Rel(skilldir, path)
			if err != nil {
				return err
			}
			realSkillID := filepath.Base(skilldir)
			agentSkillDir := "/tmp/codingmatrix-project-tpl/.ai-ready/skills/"
			cfs = append(cfs, taskflow.ConfigFile{
				Path:    filepath.Join(agentSkillDir, realSkillID, relPath),
				Content: string(content),
			})
			return nil
		})
	}
	return coding, cfs, nil
}

// GetPublic implements domain.TaskUsecase.
func (a *TaskUsecase) GetPublic(ctx context.Context, _ *domain.User, id uuid.UUID) (*domain.Task, error) {
	t, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errcode.ErrNotFound.Wrap(err)
	}

	return cvt.From(t, &domain.Task{}), nil
}

// GitTask implements domain.TaskUsecase.
func (a *TaskUsecase) GitTask(ctx context.Context, id uuid.UUID) (*domain.GitTask, error) {
	if a.taskHook != nil {
		return a.taskHook.GitTask(ctx, id)
	}
	return nil, errcode.ErrNotFound
}

// buildLLMList 构建备用 LLM 配置列表 (用于账号接力)
// 收集用户的所有模型配置 (排除当前使用的模型)，按权重排序
func (a *TaskUsecase) buildLLMList(ctx context.Context, userID uuid.UUID, currentModel *db.Model) []taskflow.LLM {
	if a.relayManager == nil {
		return nil
	}

	models, _, err := a.modelRepo.List(ctx, userID, domain.CursorReq{Limit: 100})
	if err != nil {
		a.logger.WarnContext(ctx, "failed to list models for relay", "error", err)
		return nil
	}

	var llmList []taskflow.LLM
	for _, m := range models {
		// 跳过当前使用的模型
		if m.ID == currentModel.ID {
			continue
		}
		// 跳过健康检查失败的模型
		if !m.LastCheckSuccess {
			continue
		}
		// 检查账号是否已耗尽
		accountKey := fmt.Sprintf("%s:%s", m.Provider, m.Model)
		if a.relayManager.IsAccountExhausted(ctx, accountKey) {
			a.logger.DebugContext(ctx, "skipping exhausted account", "account", accountKey)
			continue
		}

		llmList = append(llmList, taskflow.LLM{
			ApiKey:  m.APIKey,
			BaseURL: m.BaseURL,
			Model:   m.Model,
		})
	}

	return llmList
}

// RelayTask 执行账号接力: 保存上下文 -> 切换账号 -> 重启任务
//
// 当检测到当前账号额度耗尽时调用此方法:
// 1. 保存当前任务的上下文快照到 Redis
// 2. 从备用 LLM 列表中选择下一个可用账号
// 3. 用新账号的配置重启任务，并注入接力上下文
func (a *TaskUsecase) RelayTask(ctx context.Context, taskID uuid.UUID, errMsg string) error {
	if a.relayManager == nil || !a.relayManager.ShouldRelay(ctx, taskID, 0) {
		return fmt.Errorf("relay not enabled or max count reached")
	}

	// 检查是否为额度耗尽错误
	if !relay.IsQuotaExhausted(errMsg) {
		a.logger.DebugContext(ctx, "error is not quota exhaustion, skipping relay",
			"task_id", taskID, "error", errMsg)
		return nil
	}

	// 获取任务信息
	t, err := a.repo.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}
	tk := cvt.From(t, &domain.Task{})

	// 获取接力次数
	relayCount, err := a.relayManager.GetRelayCount(ctx, taskID)
	if err != nil {
		a.logger.WarnContext(ctx, "failed to get relay count", "error", err)
		relayCount = 1
	}

	if !a.relayManager.ShouldRelay(ctx, taskID, relayCount) {
		return fmt.Errorf("max relay count (%d) reached for task %s", relayCount, taskID)
	}

	// 标记当前账号为已耗尽
	if tk.Model != nil {
		accountKey := fmt.Sprintf("%s:%s", tk.Model.Provider, tk.Model.Model)
		a.relayManager.MarkAccountExhausted(ctx, accountKey)
	}

	// 加载上次用户输入
	lastInput, _ := a.relayManager.LoadLastInput(ctx, taskID)
	if lastInput == "" {
		lastInput = tk.Content
	}

	// 保存上下文快照
	snapshot := &relay.ContextSnapshot{
		TaskID:         taskID,
		OriginalPrompt: lastInput,
		Summary:        tk.Summary,
		PreviousAccount: func() string {
			if tk.Model != nil {
				return fmt.Sprintf("%s/%s", tk.Model.Provider, tk.Model.Model)
			}
			return ""
		}(),
		RelayCount: relayCount,
		CreatedAt:  time.Now().Unix(),
	}
	if err := a.relayManager.SaveContext(ctx, snapshot); err != nil {
		a.logger.ErrorContext(ctx, "failed to save relay context", "error", err)
	}

	// 构建接力提示词并注入系统提示
	relayPrompt := relay.BuildRelayPrompt(snapshot)
	if tk.VirtualMachine != nil {
		// 通过 taskflow 重启任务，加载之前的会话
		err := a.taskflow.TaskManager().Restart(ctx, taskflow.RestartTaskReq{
			ID:          taskID,
			LoadSession: true,
		})
		if err != nil {
			a.logger.ErrorContext(ctx, "failed to restart task for relay", "error", err)
			return fmt.Errorf("restart task for relay: %w", err)
		}

		// 发送接力上下文作为新的用户输入
		if err := a.taskflow.TaskManager().Continue(ctx, taskflow.TaskReq{
			VirtualMachine: &taskflow.VirtualMachine{
				ID:            tk.VirtualMachine.ID,
				HostID:        tk.VirtualMachine.Host.ID,
				EnvironmentID: tk.VirtualMachine.EnvironmentID,
			},
			Task: &taskflow.Task{
				ID:   taskID,
				Text: relayPrompt,
			},
		}); err != nil {
			a.logger.ErrorContext(ctx, "failed to send relay context", "error", err)
		}
	}

	a.logger.InfoContext(ctx, "account relay completed",
		"task_id", taskID,
		"relay_count", relayCount,
		"previous_account", snapshot.PreviousAccount,
	)

	return nil
}
