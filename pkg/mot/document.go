package mot

import (
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/bson"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

// ParseDocument 将 SDK 输入规范化为 bson.D。
func ParseDocument(input any) (bson.D, error) {
	switch v := input.(type) {
	case nil:
		return bson.D{}, nil
	case string:
		doc, err := pkgmongo.ParseBsonDoc(v)
		if err != nil {
			return nil, fmt.Errorf("%w: parse document: %w", ErrInvalidOptions, err)
		}
		return doc, nil
	case bson.D:
		return append(bson.D(nil), v...), nil
	case bson.M:
		return mapToBSOND(map[string]any(v)), nil
	case map[string]any:
		return mapToBSOND(v), nil
	default:
		return nil, invalidOptions("unsupported document input type %T", input)
	}
}

func mapToBSOND(input map[string]any) bson.D {
	if len(input) == 0 {
		return bson.D{}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	doc := make(bson.D, 0, len(keys))
	for _, key := range keys {
		doc = append(doc, bson.E{Key: key, Value: input[key]})
	}
	return doc
}

func isEmptyDocument(doc bson.D) bool {
	return len(doc) == 0
}
