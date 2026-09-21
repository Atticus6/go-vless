//go:build notunnel

package update

// selfUpdateSupported 精简构建（serverless 瘦身）不支持自更新.
const selfUpdateSupported = false
