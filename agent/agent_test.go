package agent

import (
	"testing"

	"github.com/LouYuanbo1/ai-spider/spider"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTMLModeDefault 锁定 html_tool 的默认取数模式为 accessibility：
//  1. 生成的实际 jsonschema 中 enum 完整含 accessibility/structured/full 三值，
//     且首个可选项必须是 accessibility（避免模型把 structured 当默认首选）；
//  2. default 值必须为 accessibility；
//  3. spider.DefaultHTMLMode 在空 mode 时回退到 accessibility，其余模式原样保留。
func TestHTMLModeDefault(t *testing.T) {
	t.Run("jsonschema 声明默认 accessibility 且 enum 完整有序", func(t *testing.T) {
		info, err := utils.GoStruct2ToolInfo[spider.HTML]("html_tool", "test")
		require.NoError(t, err)
		js, err := info.ParamsOneOf.ToJSONSchema()
		require.NoError(t, err)

		modePtr, ok := js.Properties.Get("mode")
		require.True(t, ok, "工具参数中应含 mode")
		mode := modePtr

		assert.Equal(t, []any{"accessibility", "structured", "full"}, mode.Enum,
			"enum 必须完整且 accessibility 排第一，否则模型易把 structured 当默认")
		assert.Equal(t, "accessibility", mode.Default, "default 必须是 accessibility")
	})

	t.Run("空 mode 回退 accessibility", func(t *testing.T) {
		assert.Equal(t, spider.HTMLModeAccessibility, spider.DefaultHTMLMode(""))
		assert.Equal(t, spider.HTMLMode(spider.HTMLModeStructured), spider.DefaultHTMLMode(spider.HTMLModeStructured))
		assert.Equal(t, spider.HTMLMode(spider.HTMLModeFull), spider.DefaultHTMLMode(spider.HTMLModeFull))
	})
}
