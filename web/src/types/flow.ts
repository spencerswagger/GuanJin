// Flow Descriptor 契约类型（decisions.md D7 + CH-2）
// 字段权威 = 页面规格 §4.1 + D7 扩展；端点行为与错误码权威 = 接口清单 §3.1.4/§3.1.5。
// 注意：CH-2 追认后新增的字段一律标 optional，保证向后兼容（纯加字段、不删既有键）。

/** 挑战用途六值词表（D2 冻结；null 表示"未指定/内部兜底"） */
export type Purpose = 'login' | 'register' | 'step_up' | 'enroll' | 'recover' | 'verify_identifier'

/** 保证级别（D12：对外只 AAL1 / AAL2；AAL3 预留、本期任何路径不得产出） */
export type Acr = 'AAL1' | 'AAL2'

/** 客户端档位（索引 §2：只有一层档位，默认 L2） */
export type Tier = 'L1' | 'L2' | 'L3'

/** 因子类型是开放集合（不加 CHECK），V1 四类 */
export type FactorType = 'password' | 'totp' | 'email_otp' | 'sms_otp' | (string & {})

export interface FlowStep {
  factor: FactorType
  /** 因子角色：holding（持有型）/ channel（信道型） */
  role: 'holding' | 'channel'
  status: 'pending' | 'done' | 'disabled'
  challenge_hint?: string | null
  /** D7 / CH-2：该步已预签挑战时非空，前端据此直接提交 */
  challenge_id?: string | null
  /** D7 / CH-2：该步的用途；register 不进 Descriptor（D2） */
  purpose?: Exclude<Purpose, 'register'>
  /** D7 / CH-2：该步完成后预计达到的级别 */
  contributes_acr?: Acr | null
}

/** D7：顶层终态必须显式枚举，前端**不得**用 disabled_factors 自行推断（否则会死循环重渲染） */
export type TerminalState = 'satisfied' | 'need_more' | 'unreachable'

export interface UnreachableReason {
  code: 'FACTORS_DISABLED' | 'POLICY_UNSATISFIABLE'
  missing_factors: FactorType[]
}

export interface FlowDescriptor {
  flow_id: string
  client_id: string
  target_acr: Acr
  current_acr: Acr | null
  satisfied: boolean
  terminal_state: TerminalState
  steps: FlowStep[]
  disabled_factors: FactorType[]
  expires_at: string
  unreachable_reason?: UnreachableReason
}

/** 统一错误响应四字段（httpx.ErrorResponse 的前端镜像） */
export interface ApiError {
  code: string
  message: string
  /** 可复制 requestId；后端按此打详细日志 */
  trace_id: string
  details?: unknown
}

/** ACR_INSUFFICIENT 的 details 形态（D3-E / AC-FLOW-02：403 不是 500/401/空白） */
export interface ACRInsufficientDetails {
  flow: FlowDescriptor
}
