package web

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// humanize 字段由 config.yaml 的 spider.humanize 控制，在 NewWeaver 创建网时注入。
// 开启时：打字逐字+随机停顿、偶发打错、点击前 hover 停顿、分段式滚动；
// 关闭时：退化为原有的一次性输入/单次点击/瞬时滚动。

// 打字节奏参数（毫秒）
const (
	typeDelayMinMs = 40    // 单字符最小间隔
	typeDelayMaxMs = 220   // 单字符最大间隔
	typoProb       = 0.025 // 每个字符打错的概率
)

// randChance 以概率 p 返回 true，用于决定是否触发一次"打错"。
func randChance(p float64) bool {
	return rand.Float64() < p
}

// nearestTypoKey 在目标字符附近随机取一个"容易按错的"字母当作输入事故。
// 这里用一个常见的字母池近似，避免依赖具体键盘布局；runes 会先排除原字符。
func nearestTypoKey(r rune) input.Key {
	pool := []rune("abcdefghijklmnopqrstuvwxyz")
	candidate := pool[rand.IntN(len(pool))]
	if candidate == r {
		// 换个候选，避免"打错"和原字符相同而看不出效果
		candidate = pool[(rand.IntN(len(pool))+13)%len(pool)]
	}
	return input.Key(candidate)
}

// typeLikeHuman 分两种方式模拟真人输入：
//   - 可打印键盘字符（ASCII 0x20~0x7E）：用底层按键事件逐字派发，字符间随机停顿，偶发打错后退格重打。
//   - 非键盘字符（中文/emoji/CJK 等）：无法用按键事件派发（CDP 只有真实键盘按键），
//     累积后改用 Input.insertText 整批插入，批间同样随机停顿，以逼近真人输入节奏。
//
// clearFlag 为 true 时先清空目标原有内容（全选再覆盖）。
func typeLikeHuman(el *rod.Element, text string, clearFlag bool) error {
	if clearFlag {
		if err := el.SelectAllText(); err != nil {
			return err
		}
		humanDelay(120, 400)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// pending 累积连续的非键盘字符，整批插入
	var pending strings.Builder
	flush := func() error {
		if pending.Len() == 0 {
			return nil
		}
		if err := el.Input(pending.String()); err != nil {
			return err
		}
		pending.Reset()
		humanDelay(80, 250)
		return nil
	}

	for _, r := range text {
		// 仅可打印键盘字符才走逐字按键；其余（中文等）进入待插入缓冲
		if r >= 0x20 && r <= 0x7E {
			if err := flush(); err != nil {
				return err
			}
			// 偶发打错：先输入一个错误字符，稍作停顿后退格删掉再继续
			if randChance(typoProb) {
				if err := el.Type(nearestTypoKey(r)); err != nil {
					return err
				}
				humanDelay(80, 220)
				if err := el.Type(input.Backspace); err != nil {
					return err
				}
				humanDelay(80, 220)
			}

			if err := el.Type(input.Key(r)); err != nil {
				return err
			}
			humanDelay(typeDelayMinMs, typeDelayMaxMs)
		} else {
			pending.WriteRune(r)
		}
	}
	return flush()
}

// typeText 将文本输入到元素；clearFlag 为 true 时先清空原有内容。
// 使用 rod 原生键盘事件触发真实输入，对 React/Vue 受控组件友好。
// 开启 humanize 时走逐字打字（见 typeLikeHuman），否则一次性写入。
func (w *Web) typeText(el *rod.Element, text string, clearFlag bool) error {
	if !w.humanize {
		if clearFlag {
			if err := el.SelectAllText(); err != nil {
				return err
			}
		}
		return el.Input(text)
	}
	return typeLikeHuman(el, text, clearFlag)
}

// elementValue 读取输入控件当前值，作为输入生效校验的依据。
// 同时兼容 input/textarea 的 value，与 contenteditable 的 textContent。
func elementValue(el *rod.Element) (string, error) {
	res, err := el.Eval(`() => this.value ?? this.textContent ?? ""`)
	if err != nil {
		return "", err
	}
	if res == nil || res.Value.Nil() {
		return "", nil
	}
	return strings.TrimSpace(res.Value.String()), nil
}

func inputEffective(el *rod.Element, text string) bool {
	got, err := elementValue(el)
	if err != nil {
		return true
	}
	return strings.Contains(got, strings.TrimSpace(text))
}

func (w *Web) inputToElement(el *rod.Element, text string, clearFlag bool) (bool, error) {
	if err := w.typeText(el, text, clearFlag); err != nil {
		return false, err
	}
	return inputEffective(el, text), nil
}

// DoInput 在网持有的页面上以 CSS 选择器定位输入框输入文本；返回是否真正写入成功
func (w *Web) doInput(ctx context.Context, a *action.Input) (bool, error) {
	page := w.Page.Context(ctx)
	var el *rod.Element
	var err error
	switch a.Type {
	case action.SelectorTypeCSS:
		el, err = page.Element(a.Selector)
	case action.SelectorTypeXPath:
		el, err = page.ElementX(a.Selector)
	default:
		return false, fmt.Errorf("未知选择器类型 %q", a.Type)
	}
	if err != nil {
		return false, fmt.Errorf("未找到输入框 %q: %v", a.Selector, err)
	}
	return w.inputToElement(el, a.Text, a.Clear)
}

func (w *Web) DoInput(
	ctx context.Context,
	a *action.Input,
) (bool, error) {
	return w.timeoutResultWrapper(
		ctx,
		a,
		w.doInput,
	)
}

// DoInputByAX 在网持有的页面上通过可访问性语义（role/name）或后端 DOM 节点 ID 精准定位输入框并输入文本；
// 返回是否真正写入成功
func (w *Web) doInputByAX(
	ctx context.Context,
	a *action.InputByAXTree,
) (bool, error) {
	page := w.Page.Context(ctx)
	var err error
	backendDOMNodeID := a.BackendDOMNodeID
	if backendDOMNodeID == 0 {
		if a.Role == "" && a.Name == "" {
			return false, fmt.Errorf("InputByAXTree 缺少定位信息：node_id、role、name 至少提供一个")
		}
		backendDOMNodeID, err = getNodeIDByRoleAndName(page, a.Role, a.Name, a.Nth)
		if err != nil {
			return false, fmt.Errorf("获取节点 %d 失败: %v", backendDOMNodeID, err)
		}
	}
	el, err := page.ElementFromNode(&proto.DOMNode{BackendNodeID: backendDOMNodeID})
	if err != nil {
		return false, fmt.Errorf("解析节点 %d 失败: %v", backendDOMNodeID, err)
	}
	return w.inputToElement(el, a.Text, a.Clear)
}

func (w *Web) DoInputByAX(
	ctx context.Context,
	a *action.InputByAXTree,
) (bool, error) {
	return w.timeoutResultWrapper(
		ctx,
		a,
		w.doInputByAX,
	)
}
