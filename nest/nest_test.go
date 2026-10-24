package nest

import (
	"testing"
)

type tModel interface{ M() }

type tWeaver struct{}

func (tWeaver) M() {}

func tZero[T any, P interface{ *T }](p P) bool {
	var zero P
	return p == zero
}

// 模拟：类型集合约束带 Model + 指针形状，能否 == nil、能否自动推断 T。
type tPtrModel2[T any] interface {
	*T
	tModel
}

type tNest[T any, S interface{ *T }] struct{ slot S }

func tNewNest[T any, S interface{ *T }](slot S) tNest[T, S] { return tNest[T, S]{slot: slot} }

func (tn *tNest[T, S]) isFree() bool { return tn.slot == nil }

func TestTypeSet(t *testing.T) {
	var w *tWeaver
	if !tZero[tWeaver](w) {
		t.Fatal("nil ptr should be zero")
	}
	// 关键验证：调用处能否只传 slot，双类型参数 T/S 自动推断
	n := tNewNest(w) // T=tWeaver, S=*tWeaver
	if !n.isFree() {
		t.Fatal("nil slot should be free")
	}
	// 比较断言：== nil 在指针形状约束下可用
	if n.slot != nil {
		t.Fatal("slot should be nil-compare-able")
	}
}
