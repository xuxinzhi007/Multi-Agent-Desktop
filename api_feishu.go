package main

// 飞书：机器人配置、房间、权限自检、用户身份授权、审批卡回调。

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/config"
	"multi-agent-desktop/internal/feishu"
)

func (s *API) feishuRoster() []feishu.Named {
	if s.mgr == nil {
		return nil
	}
	list := s.mgr.List()
	out := make([]feishu.Named, 0, len(list))
	for _, a := range list {
		sn := a.Snapshot()
		out = append(out, feishu.Named{ID: sn.ID, Name: sn.Name, Status: string(sn.Status)})
	}
	return out
}

func (s *API) decideFeishu(id string, allow, remember bool) error {
	if s.mgr == nil || s.mgr.Approvals() == nil {
		return fmt.Errorf("批准还没接上")
	}
	return s.mgr.Approvals().Decide(id, allow, remember, "")
}

func (s *API) claimFeishu(parent, chat string, busy map[string]bool) (string, error) {
	if s.mgr == nil {
		return "", fmt.Errorf("会话还没准备好")
	}
	s.feishuPick.mu.Lock()
	prefer := s.feishuPick.byChat[chat]
	s.feishuPick.mu.Unlock()

	got, err := s.mgr.AssistPreferring(parent, prefer, busy)
	if err != nil {
		return "", err
	}
	if chat != "" && got != "" {
		s.feishuPick.mu.Lock()
		if s.feishuPick.byChat == nil {
			s.feishuPick.byChat = map[string]string{}
		}
		s.feishuPick.byChat[chat] = got
		s.feishuPick.mu.Unlock()
	}
	return got, nil
}

func (s *API) runFeishu(ctx context.Context, req feishu.RunRequest) (string, error) {
	blocks, text, err := s.blocksFrom(req)
	if err != nil {
		return "", err
	}
	return s.mgr.RunBlocks(ctx, req.AgentID, text, blocks)
}

func (s *API) showFeishu(_ context.Context, req feishu.RunRequest, reply string) error {
	blocks, text, err := s.blocksFrom(req)
	if err != nil {
		return err
	}
	return s.mgr.ShowTurn(req.AgentID, text, blocks, reply)
}

// ListFeishuRooms 返回飞书群和私聊的记录。这不是应用里的频道。
func (s *API) ListFeishuRooms() []feishu.Room {
	if s.bot == nil {
		return nil
	}
	rooms := s.bot.Rooms()
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	byID := map[string]config.FeishuBot{}
	for _, b := range bots {
		byID[b.ID] = b
	}
	for i := range rooms {
		id := rooms[i].AgentID
		if id == "" {
			if b, ok := byID[rooms[i].BotID]; ok {
				id = b.AgentID
			} else if rooms[i].BotID == "" && len(bots) == 1 {
				id = bots[0].AgentID
			}
		}
		name := ""
		if id != "" {
			if a, ok := s.mgr.Get(id); ok {
				name = a.Snapshot().Name
			}
		}
		rooms[i].AgentID = id
		rooms[i].AgentName = name
		botName := ""
		if b, ok := byID[rooms[i].BotID]; ok {
			botName = b.Name
		}
		rooms[i].BotName = s.bot.BotName(rooms[i].BotID, botName)
	}
	return rooms
}

// SetFeishuRoomAgent 从下一条消息起，这个飞书群或私聊改由另一个本机会话回答。
func (s *API) SetFeishuRoomAgent(chatID, agentID string) error {
	if s.bot == nil {
		return fmt.Errorf("飞书还没接上")
	}
	if _, ok := s.mgr.Get(agentID); !ok {
		return fmt.Errorf("会话不存在")
	}
	s.bot.BindRoom(chatID, agentID)
	return nil
}

// CheckFeishuScopes 对照发消息、表情、历史、单聊、群消息和群列表，看租户已经授予了哪些。
func (s *API) CheckFeishuScopes(id string) (FeishuScopeView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuScopeView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := feishu.InspectScopes(ctx, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuScopeView{}, err
	}
	return scopeView(rep), nil
}

// OpenFeishuScopes 打开飞书开通页，一次带上还缺的全部权限，并向管理员补一次申请。
func (s *API) OpenFeishuScopes(id string) (FeishuScopeView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuScopeView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := feishu.InspectScopes(ctx, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuScopeView{}, err
	}
	view := scopeView(rep)
	if view.OK || view.URL == "" {
		return view, nil
	}
	if err := feishu.ApplyScopes(ctx, cfg.AppID, cfg.AppSecret); err != nil {
		log.Printf("feishu scopes apply: %v", err)
	}
	if err := mygo.Shell.OpenExternal(view.URL); err != nil {
		return view, err
	}
	return view, nil
}

func (s *API) feishuBot(id string) (config.FeishuBot, error) {
	if s.settings == nil {
		return config.FeishuBot{}, fmt.Errorf("会话还没准备好")
	}
	for _, b := range s.settings.Get().FeishuBots {
		if b.ID == id {
			if b.AppID == "" || b.AppSecret == "" {
				return config.FeishuBot{}, fmt.Errorf("先保存 App ID 和 App Secret")
			}
			return b, nil
		}
	}
	return config.FeishuBot{}, fmt.Errorf("机器人不存在")
}

// ListFeishu 返回全部飞书机器人和各自的长连接状态。
func (s *API) ListFeishu() []FeishuView {
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	out := make([]FeishuView, 0, len(bots))
	for _, b := range bots {
		out = append(out, s.feishuView(b))
	}
	return out
}

// SaveFeishu 保存一个机器人，并只重启这一个的长连接。
func (s *API) SaveFeishu(in FeishuSave) (FeishuView, error) {
	if s.settings == nil {
		return FeishuView{}, fmt.Errorf("会话还没准备好")
	}
	prev := config.FeishuBot{}
	if in.ID != "" {
		for _, b := range s.settings.Get().FeishuBots {
			if b.ID == in.ID {
				prev = b
				break
			}
		}
	}
	next := config.MergeFeishuBot(prev, config.FeishuBot{
		ID:          in.ID,
		Name:        in.Name,
		Enabled:     in.Enabled,
		AppID:       in.AppID,
		AppSecret:   in.AppSecret,
		AgentID:     in.AgentID,
		MentionOnly: in.MentionOnly,
	})
	if next.Enabled {
		if err := s.validateFeishu(next); err != nil {
			return FeishuView{}, err
		}
	}
	saved, err := s.settings.SaveFeishuBot(next)
	if err != nil {
		return FeishuView{}, err
	}
	s.syncFeishu()
	return s.feishuView(saved), nil
}

// SetFeishuEnabled 立刻只改这一个机器人的启用，并拉起或停掉它的长连接。
func (s *API) SetFeishuEnabled(id string, enabled bool) (FeishuView, error) {
	if s.settings == nil {
		return FeishuView{}, fmt.Errorf("会话还没准备好")
	}
	var cur config.FeishuBot
	found := false
	for _, b := range s.settings.Get().FeishuBots {
		if b.ID == id {
			cur = b
			found = true
			break
		}
	}
	if !found {
		return FeishuView{}, fmt.Errorf("机器人不存在")
	}
	cur.Enabled = enabled
	if cur.Enabled {
		if err := s.validateFeishu(cur); err != nil {
			return FeishuView{}, err
		}
	}
	saved, err := s.settings.SaveFeishuBot(cur)
	if err != nil {
		return FeishuView{}, err
	}
	s.syncFeishu()
	return s.feishuView(saved), nil
}

// DeleteFeishu 停掉这个机器人的长连接并删掉配置。
func (s *API) DeleteFeishu(id string) error {
	if s.settings == nil {
		return fmt.Errorf("会话还没准备好")
	}
	if err := s.settings.DeleteFeishuBot(id); err != nil {
		return err
	}
	s.syncFeishu()
	return nil
}

func (s *API) validateFeishu(bot config.FeishuBot) error {
	if bot.AppID == "" || bot.AppSecret == "" || bot.AgentID == "" {
		return fmt.Errorf("启用前要填写 App ID、App Secret，并选择一个会话")
	}
	if s.mgr == nil {
		return fmt.Errorf("会话还没准备好")
	}
	if _, ok := s.mgr.Get(bot.AgentID); !ok {
		return fmt.Errorf("绑定的会话不存在")
	}
	return nil
}

func (s *API) syncFeishu() {
	if s.bot == nil {
		return
	}
	// 用户令牌会自动过期，刷新结果要落回配置，否则重启后又变回过期令牌。
	botRefresher = func(botID, access, refresh string, expire int64) {
		if s.settings == nil {
			return
		}
		for _, b := range s.settings.Get().FeishuBots {
			if b.ID != botID {
				continue
			}
			b.UserAccess = access
			b.UserRefresh = refresh
			b.UserExpire = expire
			if _, err := s.settings.SaveFeishuBot(b); err != nil {
				log.Printf("feishu user token: 保存失败 %v", err)
			}
			return
		}
	}
	bots := []config.FeishuBot{}
	if s.settings != nil {
		bots = s.settings.Get().FeishuBots
	}
	list := make([]feishu.Bot, 0, len(bots))
	on := false
	for _, b := range bots {
		if b.Enabled {
			on = true
		}
		list = append(list, feishu.Bot{ID: b.ID, Config: feishuConfig(b)})
	}
	if s.mgr != nil {
		s.mgr.SetFeishuOn(on)
	}
	s.bot.Sync(list)
}

func (s *API) feishuView(cfg config.FeishuBot) FeishuView {
	view := FeishuView{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Enabled:     cfg.Enabled,
		AppID:       cfg.AppID,
		HasSecret:   cfg.AppSecret != "",
		HasUser:     cfg.UserAccess != "",
		AgentID:     cfg.AgentID,
		MentionOnly: cfg.MentionOnly,
		Status:      "stopped",
		UserExpire:  cfg.UserExpire,
	}
	if s.bot != nil {
		st := s.bot.Status(cfg.ID)
		if st.State != "" {
			view.Status = st.State
		}
		view.Detail = st.Detail
	}
	return view
}

// BeginFeishuUserAuth 申请设备授权码，并把确认页打开。
// 用户点「同意」之后，前端再调 FinishFeishuUserAuth 把令牌取回来。
func (s *API) BeginFeishuUserAuth(ctx context.Context, id string) (FeishuAuthView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuAuthView{}, err
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return FeishuAuthView{}, fmt.Errorf("先保存 App ID 和 App Secret")
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d, err := feishu.StartDeviceAuth(call, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return FeishuAuthView{}, err
	}
	s.authMu.Lock()
	if s.auths == nil {
		s.auths = map[string]feishu.DeviceAuth{}
	}
	s.auths[id] = d
	s.authMu.Unlock()
	_ = mygo.Shell.OpenExternal(d.VerifyURL)
	return FeishuAuthView{URL: d.VerifyURL, Code: d.UserCode, ExpiresIn: int(d.Expires.Seconds())}, nil
}

// FinishFeishuUserAuth 等用户确认，把用户令牌留在本机配置里。
func (s *API) FinishFeishuUserAuth(ctx context.Context, id string) (FeishuView, error) {
	cfg, err := s.feishuBot(id)
	if err != nil {
		return FeishuView{}, err
	}
	s.authMu.Lock()
	d, ok := s.auths[id]
	s.authMu.Unlock()
	if !ok {
		return FeishuView{}, fmt.Errorf("先点「用我的飞书身份授权」拿到验证码")
	}
	// 设备码十分钟有效，但一次调用等十分钟太久，先等三分钟。
	wait := d.Expires
	if wait > 3*time.Minute {
		wait = 3 * time.Minute
	}
	call, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	access, refresh, exp, err := feishu.WaitDeviceAuth(call, cfg.AppID, cfg.AppSecret, d)
	if err != nil {
		return FeishuView{}, err
	}
	cfg.UserAccess = access
	cfg.UserRefresh = refresh
	cfg.UserExpire = exp.Unix()
	// 立刻验一次：授权成功不等于能读群消息，用户要马上看到结论。
	who, werr := feishu.WhoAmI(ctx, feishu.Cred{
		AppID: cfg.AppID, Secret: cfg.AppSecret,
		UserToken: cfg.UserAccess, UserRefresh: cfg.UserRefresh, UserExpire: cfg.UserExpire,
	})
	if werr != nil {
		log.Printf("feishu whoami: %v", werr)
	}
	probe := probeUserRead(ctx, cfg)
	saved, err := s.settings.SaveFeishuBot(cfg)
	if err != nil {
		return FeishuView{}, err
	}
	s.authMu.Lock()
	delete(s.auths, id)
	s.authMu.Unlock()
	s.syncFeishu()
	view := s.feishuView(saved)
	if who.Name != "" {
		view.UserName = who.Name
	}
	view.UserRead = probe
	return view, nil
}

// SyncFeishuChat 按 chat_id 拉一次群历史，返回新增条数。
//
// 这是「显式放行」入口：默认只同步机器人在的群，机器人不在的群不会自动读；
// 手动读一次之后，这个群会被记进配置的放行名单，之后继续补历史（仍然只读）。
func (s *API) SyncFeishuChat(chatID string) (int, error) {
	if s.bot == nil {
		return 0, fmt.Errorf("飞书还没接上")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := s.bot.SyncRoom(ctx, chatID)
	if err != nil {
		return n, err
	}
	s.allowFeishuChat(chatID)
	return n, nil
}

// allowFeishuChat 把手动读过的群记进配置，视为显式放行。
func (s *API) allowFeishuChat(chatID string) {
	if s.settings == nil || strings.TrimSpace(chatID) == "" {
		return
	}
	chatID = strings.TrimSpace(chatID)
	for _, b := range s.settings.Get().FeishuBots {
		if b.UserAccess == "" {
			continue
		}
		for _, id := range b.AllowChats {
			if id == chatID {
				return
			}
		}
		b.AllowChats = append(b.AllowChats, chatID)
		if _, err := s.settings.SaveFeishuBot(b); err != nil {
			log.Printf("feishu allow chat: %v", err)
			return
		}
		s.syncFeishu()
		return
	}
}

// FeishuMedia 下载一条消息里的图片，返回可以直接放进 img 的 data URL。
// 前端只在要显示缩略图时调用；失败时把飞书的原因原样带出去。
func (s *API) FeishuMedia(messageID, key string) (string, error) {
	if s.bot == nil {
		return "", fmt.Errorf("飞书还没接上")
	}
	if messageID == "" || key == "" {
		return "", fmt.Errorf("缺少图片信息")
	}
	cacheKey := messageID + "\n" + key
	s.mediaMu.Lock()
	if hit, ok := s.media[cacheKey]; ok && time.Since(hit.at) < 30*time.Minute {
		url := hit.url
		s.mediaMu.Unlock()
		return url, nil
	}
	s.mediaMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, mime, err := s.bot.FetchMedia(ctx, messageID, key, "image")
	if err != nil {
		return "", err
	}
	if len(raw) > 4<<20 {
		return "", fmt.Errorf("图片太大（%d KB），没有加载", len(raw)/1024)
	}
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	s.mediaMu.Lock()
	if s.media == nil || len(s.media) > 256 {
		s.media = map[string]mediaBlob{}
	}
	s.media[cacheKey] = mediaBlob{url: url, at: time.Now()}
	s.mediaMu.Unlock()
	return url, nil
}
