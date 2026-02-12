package mongo

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
)

// ParseBsonDoc 将 JSON/ExtJSON 字符串解析为 bson.D 格式。
//
// 入参:
// - jsonStr: JSON 或 ExtJSON 格式字符串，如 {"status":"inactive"} 或 {"$set":{"status":"archived"}}
//
// 出参:
// - bson.D: 解析后的 BSON 文档
// - error: JSON/ExtJSON 解析失败时非 nil
//
// 注意: 空字符串或 "{}" 返回空的 bson.D（匹配所有文档），其余使用 bson.UnmarshalExtJSON 解析。
//
//	同时适用于解析 filter 和 update 参数。
func ParseBsonDoc(jsonStr string) (bson.D, error) {
	if jsonStr == "" || jsonStr == "{}" {
		return bson.D{}, nil
	}

	var result bson.D
	if err := bson.UnmarshalExtJSON([]byte(jsonStr), false, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON/ExtJSON: %w", err)
	}
	return result, nil
}
