package mongo

import (
	"fmt"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
)

// ---------- 预编译正则，避免每次调用重复编译 ----------

var (
	// reUnquotedKey 匹配紧跟在 { 或 , 后面的无引号键名（可含 $、.）。
	reUnquotedKey = regexp.MustCompile(`([{,])\s*(\$?[a-zA-Z_][\w.$]*)\s*:`)

	// reISODate 匹配 ISODate("...") shell 函数。
	reISODate = regexp.MustCompile(`ISODate\(\s*"([^"]+)"\s*\)`)

	// reNewDate 匹配 new Date("...") shell 函数。
	reNewDate = regexp.MustCompile(`new\s+Date\(\s*"([^"]+)"\s*\)`)

	// reObjectId 匹配 ObjectId("...") shell 函数。
	reObjectId = regexp.MustCompile(`ObjectId\(\s*"([^"]+)"\s*\)`)

	// reNumberLong 匹配 NumberLong(123) 或 NumberLong("123") shell 函数。
	reNumberLong = regexp.MustCompile(`NumberLong\(\s*"?(\d+)"?\s*\)`)

	// reNumberInt 匹配 NumberInt(42) 或 NumberInt("42") shell 函数。
	reNumberInt = regexp.MustCompile(`NumberInt\(\s*"?(\d+)"?\s*\)`)

	// reNumberDecimal 匹配 NumberDecimal("1.23") shell 函数。
	reNumberDecimal = regexp.MustCompile(`NumberDecimal\(\s*"([^"]+)"\s*\)`)

	// reTimestamp 匹配 Timestamp(sec, inc) shell 函数。
	reTimestamp = regexp.MustCompile(`Timestamp\(\s*(\d+)\s*,\s*(\d+)\s*\)`)

	// reTrailingComma 匹配 } 或 ] 前面的尾部逗号。
	reTrailingComma = regexp.MustCompile(`,\s*([}\]])`)
)

// segment 表示输入字符串按双引号边界拆分后的一个片段。
type segment struct {
	content  string // 片段内容
	isString bool   // 是否为双引号字符串（含引号本身）
}

// ShellToExtJSON 将 MongoDB Shell 风格的查询字符串转换为合法的 Extended JSON。
//
// 入参:
// - input: 原始输入字符串，可以是标准 JSON、ExtJSON 或 Shell 语法
//
// 出参:
// - string: 转换后的 Extended JSON 字符串
//
// 注意:
//   - 对已合法的 JSON/ExtJSON 输入具有幂等性（不改变内容）。
//   - 处理顺序：单引号转双引号 → shell 函数替换（全字符串） → 分段 → 仅对非字符串段做键名加引号/尾部逗号清理 → 拼接。
//   - 支持 ISODate、new Date、ObjectId、NumberLong、NumberInt、NumberDecimal、Timestamp。
func ShellToExtJSON(input string) string {
	input = strings.TrimSpace(input)
	if input == "" || input == "{}" {
		return input
	}

	// 第一步：将单引号字符串转为双引号字符串
	input = singleToDoubleQuotes(input)

	// 第二步：替换 shell 函数调用（在完整字符串上操作，因为函数参数含双引号跨越字符串边界）
	// 安全性：函数参数内的 \" 转义使得正则不会匹配到 JSON 字符串值内部的假阳性
	input = replaceShellFunctions(input)

	// 第三步：按双引号边界拆分为 string / non-string 段
	segments := splitByStrings(input)

	// 第四步：仅对非字符串段应用键名加引号和尾部逗号清理
	var buf strings.Builder
	buf.Grow(len(input) + 64)
	for _, seg := range segments {
		if seg.isString {
			buf.WriteString(seg.content)
		} else {
			buf.WriteString(quoteKeysAndCleanup(seg.content))
		}
	}

	return buf.String()
}

// singleToDoubleQuotes 将输入中的单引号字符串转为双引号字符串。
//
// 入参:
// - input: 可能包含单引号字符串的原始输入
//
// 出参:
// - string: 所有单引号字符串已被替换为双引号字符串
//
// 注意:
//   - 逐字符扫描，遇到单引号开始收集字符串内容直到匹配的关闭单引号。
//   - 单引号内的 \' 转义还原为 '，内部的双引号 " 转义为 \"。
//   - 双引号字符串区间内不做任何处理，原样保留。
func singleToDoubleQuotes(input string) string {
	var buf strings.Builder
	buf.Grow(len(input))

	i := 0
	runes := []rune(input)
	n := len(runes)

	for i < n {
		ch := runes[i]

		// 跳过双引号字符串区间，原样保留
		if ch == '"' {
			buf.WriteRune(ch)
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					buf.WriteRune(runes[i])
					buf.WriteRune(runes[i+1])
					i += 2
					continue
				}
				buf.WriteRune(runes[i])
				if runes[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}

		// 遇到单引号，转换为双引号字符串
		if ch == '\'' {
			buf.WriteRune('"')
			i++ // 跳过开始单引号
			for i < n {
				if runes[i] == '\\' && i+1 < n && runes[i+1] == '\'' {
					// \' → '（去掉转义）
					buf.WriteRune('\'')
					i += 2
					continue
				}
				if runes[i] == '"' {
					// 内部双引号需要转义
					buf.WriteRune('\\')
					buf.WriteRune('"')
					i++
					continue
				}
				if runes[i] == '\'' {
					// 关闭单引号
					buf.WriteRune('"')
					i++
					break
				}
				buf.WriteRune(runes[i])
				i++
			}
			continue
		}

		buf.WriteRune(ch)
		i++
	}

	return buf.String()
}

// replaceShellFunctions 在完整字符串上替换 MongoDB Shell 函数调用为 ExtJSON 表示。
//
// 入参:
// - s: 已完成单引号→双引号转换的完整输入字符串
//
// 出参:
// - string: shell 函数已替换为 ExtJSON 的字符串
//
// 注意:
//   - 在字符串拆分前执行，因为 shell 函数参数（如 ISODate("...")）的双引号会被 splitByStrings 拆开。
//   - JSON 字符串值内部的 \" 转义可防止正则误匹配（正则要求 ISODate( 后紧跟未转义的 "）。
//   - 替换字符串中 $ 需要用 $$ 转义，避免被 Go regexp 解释为命名捕获组引用。
func replaceShellFunctions(s string) string {
	s = reISODate.ReplaceAllString(s, `{"$$date":"$1"}`)
	s = reNewDate.ReplaceAllString(s, `{"$$date":"$1"}`)
	s = reObjectId.ReplaceAllString(s, `{"$$oid":"$1"}`)
	s = reNumberLong.ReplaceAllString(s, `{"$$numberLong":"$1"}`)
	s = reNumberInt.ReplaceAllString(s, `{"$$numberInt":"$1"}`)
	s = reNumberDecimal.ReplaceAllString(s, `{"$$numberDecimal":"$1"}`)
	s = reTimestamp.ReplaceAllString(s, `{"$$timestamp":{"t":$1,"i":$2}}`)
	return s
}

// splitByStrings 按双引号边界将输入拆分为交替的非字符串段和字符串段。
//
// 入参:
// - input: 已完成单引号→双引号转换和 shell 函数替换的字符串
//
// 出参:
// - []segment: 拆分后的片段切片，交替排列 isString=false 和 isString=true
//
// 注意: 正确处理 \" 转义，不会在转义双引号处错误拆分。
func splitByStrings(input string) []segment {
	var segments []segment
	var buf strings.Builder

	runes := []rune(input)
	n := len(runes)
	i := 0

	for i < n {
		if runes[i] == '"' {
			// 先将之前累积的非字符串内容保存
			if buf.Len() > 0 {
				segments = append(segments, segment{content: buf.String(), isString: false})
				buf.Reset()
			}

			// 收集完整的双引号字符串（含引号本身）
			buf.WriteRune(runes[i])
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					buf.WriteRune(runes[i])
					buf.WriteRune(runes[i+1])
					i += 2
					continue
				}
				buf.WriteRune(runes[i])
				if runes[i] == '"' {
					i++
					break
				}
				i++
			}
			segments = append(segments, segment{content: buf.String(), isString: true})
			buf.Reset()
		} else {
			buf.WriteRune(runes[i])
			i++
		}
	}

	// 尾部非字符串内容
	if buf.Len() > 0 {
		segments = append(segments, segment{content: buf.String(), isString: false})
	}

	return segments
}

// quoteKeysAndCleanup 对非字符串段进行无引号键名加引号和尾部逗号清理。
//
// 入参:
// - s: 非字符串段内容（不含双引号包裹的部分）
//
// 出参:
// - string: 替换后的内容
//
// 注意: 替换顺序：先为无引号键名加双引号，再清理尾部逗号。
func quoteKeysAndCleanup(s string) string {
	// 1. 为无引号键名添加双引号
	s = reUnquotedKey.ReplaceAllString(s, `$1"$2":`)

	// 2. 清理尾部逗号
	s = reTrailingComma.ReplaceAllString(s, `$1`)

	return s
}

// ParseBsonDoc 将 JSON/ExtJSON/Shell 语法字符串解析为 bson.D 格式。
//
// 入参:
// - jsonStr: JSON、ExtJSON 或 MongoDB Shell 格式字符串，
//
//	如 {"status":"inactive"}、{"$set":{"status":"archived"}} 或 {hitCreateTime: {$lt: ISODate("2024-01-01T00:00:00Z")}}
//
// 出参:
// - bson.D: 解析后的 BSON 文档
// - error: 解析失败时非 nil
//
// 注意:
//   - 空字符串或 "{}" 返回空的 bson.D（匹配所有文档）。
//   - 内部先通过 ShellToExtJSON 将 shell 语法转为 ExtJSON，再调用 bson.UnmarshalExtJSON 解析。
//   - 同时适用于解析 filter 和 update 参数。
func ParseBsonDoc(jsonStr string) (bson.D, error) {
	if jsonStr == "" || jsonStr == "{}" {
		return bson.D{}, nil
	}

	// 预处理: Shell 语法 → ExtJSON
	extJSON := ShellToExtJSON(jsonStr)

	var result bson.D
	if err := bson.UnmarshalExtJSON([]byte(extJSON), false, &result); err != nil {
		hint := ""
		if open, close := strings.Count(extJSON, "{"), strings.Count(extJSON, "}"); open != close {
			hint = fmt.Sprintf("\n  hint: mismatched braces ('{' x%d vs '}' x%d), please check your input", open, close)
		}
		return nil, fmt.Errorf("invalid JSON/ExtJSON/Shell syntax: %w\n  original:  %s\n  converted: %s%s", err, jsonStr, extJSON, hint)
	}
	return result, nil
}
