package main

import "sync"

// surface 是主窗口和悬浮岛共用的那几个动作。
// 测试用假窗口，正式路径用 *mygo.Window。
type surface interface {
	Show()
	Hide()
	Focus()
	Destroy()
	IsDestroyed() bool
	IsVisible() bool
}

// islandSwitch 决定哪一块窗口活着。
// 岛不存在时才创建；回到主窗口时先让主窗口可见，再销毁岛。
type islandSwitch struct {
	mu   sync.Mutex
	main surface
	mini surface
	open func() (surface, error)
}

func (p *islandSwitch) current() surface {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mini
}

func (p *islandSwitch) clear(w surface) {
	p.mu.Lock()
	if p.mini == w {
		p.mini = nil
	}
	p.mu.Unlock()
}

func (p *islandSwitch) showMain() {
	p.mu.Lock()
	main := p.main
	p.mu.Unlock()
	if main == nil || main.IsDestroyed() {
		return
	}
	main.Show()
	main.Focus()
}

// toggle 切到岛（show）或回到主窗口。
// 回到主窗口时返回 false。两个窗口都不可见时把主窗口拉回来。
func (p *islandSwitch) toggle(show bool) bool {
	if show {
		p.mu.Lock()
		mini := p.mini
		open := p.open
		p.mu.Unlock()
		if mini == nil || mini.IsDestroyed() {
			var w surface
			var err error
			if open != nil {
				w, err = open()
			}
			if err != nil || w == nil || w.IsDestroyed() {
				p.showMain()
				return false
			}
			p.mu.Lock()
			p.mini = w
			p.mu.Unlock()
			mini = w
		}
		mini.Show()
		mini.Focus()
		p.mu.Lock()
		main := p.main
		p.mu.Unlock()
		if main != nil && !main.IsDestroyed() {
			main.Hide()
		}
		if p.stuck() {
			p.showMain()
			return false
		}
		return true
	}
	p.showMain()
	p.mu.Lock()
	mini := p.mini
	p.mini = nil
	p.mu.Unlock()
	if mini != nil && !mini.IsDestroyed() {
		mini.Destroy()
	}
	if p.stuck() {
		p.showMain()
	}
	return false
}

func (p *islandSwitch) stuck() bool {
	p.mu.Lock()
	main := p.main
	mini := p.mini
	p.mu.Unlock()
	mainUp := main != nil && !main.IsDestroyed() && main.IsVisible()
	miniUp := mini != nil && !mini.IsDestroyed() && mini.IsVisible()
	return !mainUp && !miniUp
}
