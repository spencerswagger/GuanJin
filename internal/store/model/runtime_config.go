package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RuntimeConfig 是 `runtime_config` 表（键值配置）的读写映射。
//
// 用途（decisions.md D5）：`flow.ttl_seconds` 等运行期参数的唯一权威落点；
// **主密钥不得进本表**（迁移里有 `runtime_config_no_master_key` CHECK 兜底）。
type RuntimeConfig struct {
	// Key 是主键（text）。
	Key string `gorm:"column:key;primaryKey"`
	// Value 是 jsonb（int/bool/string/object 都在这里）。
	Value JSONValue `gorm:"column:value;type:jsonb;not null"`
	// ValueType 是 int | bool | string | json | secret（DB 有 CHECK）。
	ValueType string `gorm:"column:value_type;not null;default:json"`
	// Description 是给人看的说明。
	Description *string `gorm:"column:description"`
	// IsSecret 为 true 时 value 是 SM4 密文（base64）。
	IsSecret bool `gorm:"column:is_secret;not null;default:false"`
	// UpdatedBy 记录最后修改人（可空）。
	UpdatedBy *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	// CreatedAt / UpdatedAt 归数据库（只读）。
	CreatedAt time.Time `gorm:"column:created_at;not null;->"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;->"`
}

// TableName 冻结表名（F0）。
func (RuntimeConfig) TableName() string { return "runtime_config" }

// JSONValue 是原始 jsonb 字节（读出来是数字/布尔/字符串原文，交给调用方按 value_type 解释）。
//
// 为什么不用 map[string]any：`flow.ttl_seconds` 的 value 是**标量** `600`，
// 解成 map 会直接失败；保持原始字节让"按 value_type 解释"这一步显式发生在仓储层。
type JSONValue []byte

// Value 实现 driver.Valuer。
func (v JSONValue) Value() (driver.Value, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("model: empty runtime_config value")
	}
	if !json.Valid(v) {
		return nil, fmt.Errorf("model: runtime_config value is not valid json: %s", string(v))
	}
	return string(v), nil
}

// Scan 实现 sql.Scanner。
func (v *JSONValue) Scan(src any) error {
	if v == nil {
		return fmt.Errorf("model: nil JSONValue target")
	}
	switch raw := src.(type) {
	case nil:
		*v = nil
		return nil
	case []byte:
		*v = append((*v)[:0], raw...)
		return nil
	case string:
		*v = append((*v)[:0], raw...)
		return nil
	default:
		return fmt.Errorf("model: unsupported jsonb source type %T", src)
	}
}

// AsInt 把 jsonb 数字解成 int（value_type='int' 的标量）。
//
// 不接受 `"600"`（带引号的字符串）：type 说 int，值写成字符串属数据错误，
// 静默接受会让"配置写错"变成运行期难查的偏差。
func (v JSONValue) AsInt() (int, error) {
	// 先卡字面形态：encoding/json 允许把 JSON **字符串**解成 json.Number
	// （`"600"` 会被悄悄接受），那样"type=int 但值写成字符串"这种配置错误
	// 就永远不会被发现。这里要求第一个非空字符必须是数字或负号。
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return 0, fmt.Errorf("model: runtime_config value is not a json number: %s", string(v))
	}

	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return 0, fmt.Errorf("model: runtime_config value is not a json number: %s", string(v))
	}
	i, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("model: runtime_config value is not an integer: %s", string(v))
	}
	return int(i), nil
}

// AsString 把 jsonb 字符串解成 string。
func (v JSONValue) AsString() (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("model: runtime_config value is not a json string: %s", string(v))
	}
	return s, nil
}

// AsBool 把 jsonb 布尔解成 bool。
func (v JSONValue) AsBool() (bool, error) {
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false, fmt.Errorf("model: runtime_config value is not a json boolean: %s", string(v))
	}
	return b, nil
}

// String 便于日志（原样 json 文本）。
func (v JSONValue) String() string { return string(v) }
