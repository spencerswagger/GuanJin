<script setup lang="ts">
// 骨架页：只验证「Flow Descriptor 契约类型能编译 + 终态判定不靠猜」。
// 真实页面在后续切片（页面规格 C1–C10 / A1–A12）落地，本页不含业务请求。
import { computed, ref } from 'vue'
import type { FlowDescriptor, TerminalState } from './types/flow'

const state = ref<TerminalState>('need_more')

const demo: FlowDescriptor = {
  flow_id: 'demo-flow',
  client_id: 'demo-client',
  target_acr: 'AAL2',
  current_acr: 'AAL1',
  satisfied: false,
  terminal_state: 'need_more',
  steps: [
    { factor: 'password', role: 'holding', status: 'done', purpose: 'login', contributes_acr: 'AAL1' },
    { factor: 'totp', role: 'holding', status: 'pending', challenge_id: null, contributes_acr: 'AAL2' }
  ],
  disabled_factors: [],
  expires_at: '2026-11-02T10:10:00Z'
}

// D7：终态由服务端显式给出；前端只做展示分支，**不得**自行推断"因子全禁用"。
const terminalText = computed<string>(() => {
  switch (state.value) {
    case 'satisfied':
      return '已满足，可提交授权'
    case 'need_more':
      return '还需补步（服务端会下发下一步）'
    case 'unreachable':
      return '不可达：请换业务系统或联系管理员'
    default: {
      const exhaustive: never = state.value
      return exhaustive
    }
  }
})

const pending = computed<number>(() => demo.steps.filter((s) => s.status === 'pending').length)
</script>

<template>
  <main>
    <h1>关津 GuanJin · 骨架</h1>
    <p>目标级别：{{ demo.target_acr }} ｜ 当前级别：{{ demo.current_acr }} ｜ 待补步骤：{{ pending }}</p>
    <p>流程终态：{{ terminalText }}</p>
    <p><small>flow_id={{ demo.flow_id }} · client={{ demo.client_id }}</small></p>
  </main>
</template>

<style scoped>
main {
  font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
  margin: 2rem auto;
  max-width: 40rem;
  line-height: 1.7;
}
h1 {
  font-size: 1.25rem;
}
</style>
