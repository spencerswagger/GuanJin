package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Flow 是第 15 张表 `flows`（decisions.md D5，13 列）的读写映射。
//
// 只做映射，**不承载业务规则**：amr 单调（I-F2）、一次性回填（I-F7）、锁序与
// 锁内禁令（I-F8）由 `internal/flow`（Core）在事务内校验；本模型只保证
// 列名、可空性、JSON/数组形态与迁移一致（drift 测试守住）。
type Flow struct {
	BaseModel
	// UserID 匿名流为 NULL，完成时回填（I-F1 的另一半在 DB CHECK）。
	UserID *uuid.UUID `gorm:"column:user_id;type:uuid"`
	// SessionID 登录成功后**一次性**回填（I-F7）。
	SessionID *uuid.UUID `gorm:"column:session_id;type:uuid"`
	// ClientID 必带业务系统上下文（NOT NULL）。
	ClientID string `gorm:"column:client_id;not null"`
	// TargetACR 目标档位，取值 AAL1/AAL2/AAL3（DB 有 CHECK）；V1 不产出 AAL3（D12）。
	TargetACR string `gorm:"column:target_acr;not null;default:AAL1"`
	// AchievedAMR 已达成因子集合，**单调不减**（I-F2）。
	AchievedAMR StringArray `gorm:"column:achieved_amr;type:text[];not null"`
	// CompletedSteps 已完成步骤，只追加不改写（`[{factor,challenge_id,at}]`）。
	CompletedSteps StepList `gorm:"column:completed_steps;type:jsonb;not null"`
	// Status active | completed | expired | abandoned。
	Status string `gorm:"column:status;not null;default:active"`
	// ExpiresAt 流级 TTL，**无 DB 默认值**：由 runtime_config 的 flow.ttl_seconds 计算（D5）。
	ExpiresAt time.Time `gorm:"column:expires_at;not null"`
	// ConsumedAt 一次性消费标记，与 status='completed' 同真同假（I-F4，DB CHECK）。
	ConsumedAt *time.Time `gorm:"column:consumed_at"`
	// TraceID **只写创建该流那次请求的 trace_id**，补步不覆盖（D4-C(4)）。
	TraceID *string `gorm:"column:trace_id"`
}

// TableName 冻结表名（F0：表名不得改名）。
func (Flow) TableName() string { return "flows" }

// FlowColumns 是 flows 的 13 个列名（顺序即迁移里的定义顺序），供 drift 测试与 SELECT 使用。
func FlowColumns() []string {
	return []string{
		"id", "user_id", "session_id", "client_id", "target_acr", "achieved_amr",
		"completed_steps", "status", "expires_at", "consumed_at", "trace_id",
		"created_at", "updated_at",
	}
}

// Step 是 `flows.completed_steps` 的元素。`challenge_id` 是**逻辑引用**（不加 FK，D5）。
type Step struct {
	Factor      string    `json:"factor"`
	ChallengeID string    `json:"challenge_id"`
	At          time.Time `json:"at"`
}

// StepList 是 `completed_steps` 的 jsonb 数组（只追加）。
type StepList []Step

// Value 实现 driver.Valuer（写库用）。
func (s StepList) Value() (driver.Value, error) {
	if s == nil {
		// NOT NULL 列：nil 写成空数组，而不是 NULL（否则一插入就撞约束）。
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("model: marshal completed_steps: %w", err)
	}
	return string(b), nil
}

// Scan 实现 sql.Scanner（读库用）。
func (s *StepList) Scan(src any) error {
	if s == nil {
		return fmt.Errorf("model: nil StepList target")
	}
	switch v := src.(type) {
	case nil:
		*s = StepList{}
		return nil
	case []byte:
		return s.decode(v)
	case string:
		return s.decode([]byte(v))
	default:
		return fmt.Errorf("model: unsupported completed_steps source type %T", src)
	}
}

func (s *StepList) decode(raw []byte) error {
	if len(raw) == 0 {
		*s = StepList{}
		return nil
	}
	var out []Step
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("model: decode completed_steps: %w", err)
	}
	*s = out
	return nil
}

// StringArray 是 PostgreSQL `text[]` 的读写映射（如 achieved_amr、sessions.amr）。
//
// 为什么不用 pq.StringArray：本项目的唯一驱动是 pgx（gorm postgres driver），
// 这里给一份 15 行的实现，避免再引第二个数组类型库（类型不统一会逼出转换代码）。
type StringArray []string

// Value 实现 driver.Valuer。空切片写成 `{}`（NOT NULL 列不允许 NULL）。
func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return "{}", nil
	}
	return encodePGTextArray(a), nil
}

// Scan 实现 sql.Scanner（pgx 返回的是 `{a,b}` 文本形态）。
func (a *StringArray) Scan(src any) error {
	if a == nil {
		return fmt.Errorf("model: nil StringArray target")
	}
	switch v := src.(type) {
	case nil:
		*a = StringArray{}
		return nil
	case []byte:
		*a = decodePGTextArray(string(v))
		return nil
	case string:
		*a = decodePGTextArray(v)
		return nil
	default:
		return fmt.Errorf("model: unsupported text[] source type %T", src)
	}
}

// encodePGTextArray 编码成 PostgreSQL 数组字面量 `{"a","b"}`（元素内转义 " 与 \）。
func encodePGTextArray(items []string) string {
	out := make([]byte, 0, 2+len(items)*8)
	out = append(out, '{')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '"')
		for j := 0; j < len(item); j++ {
			if item[j] == '"' || item[j] == '\\' {
				out = append(out, '\\')
			}
			out = append(out, item[j])
		}
		out = append(out, '"')
	}
	return string(append(out, '}'))
}

// decodePGTextArray 解析 `{a,b}` / `{"a","b"}` / `{}`。
//
// 只处理本项目会用到的形态：元素里若出现逗号/花括号，写库时一律加引号，
// 所以这里的"裸元素"分支只用于读取手工写入的简单值。
func decodePGTextArray(raw string) StringArray {
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return StringArray{}
	}
	body := raw[1 : len(raw)-1]
	if body == "" {
		return StringArray{}
	}
	out := make([]string, 0, 4)
	var cur []byte
	quoted := false
	escaped := false
	flush := func() {
		out = append(out, string(cur))
		cur = cur[:0]
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case escaped:
			cur = append(cur, c)
			escaped = false
		case c == '\\' && quoted:
			escaped = true
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return out
}
