package mot_test

import (
	"errors"
	"testing"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

func TestLegacyPartialErrorUnkeyedLiteralCompatibility(t *testing.T) {
	// 场景：外部包原有的三字段未命名复合字面量必须继续编译并保持 errors.Is 行为。
	err := &mot.PartialError{"bulk", mot.BulkResult{MatchedTotal: 1}, errors.New("stopped")}
	if err.Result.MatchedTotal != 1 || !errors.Is(err, mot.ErrPartialResult) {
		t.Fatalf("legacy partial error = %#v", err)
	}
}
