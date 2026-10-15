#!/usr/bin/env bash
set -euo pipefail

# 生成 base64 编码的 32 字节主密钥（>=32 字节，符合 §7.2 校验）。
openssl rand -base64 32
