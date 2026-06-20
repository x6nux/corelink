package connpool

// ComputeScore 计算综合质量分（越高越好）。
//
// 公式: score = 1000 - (rttMs * 2) - (lossPermille * 5) - (jitterMs * 3)
// 结果钳位到 [0, 1000]。
func ComputeScore(m QualityMetrics) float64 {
	rttMs := float64(m.RTT.Milliseconds())
	jitterMs := float64(m.Jitter.Milliseconds())
	score := 1000.0 - (rttMs * 2) - (float64(m.LossPermille) * 5) - (jitterMs * 3)
	if score < 0 {
		return 0
	}
	if score > 1000 {
		return 1000
	}
	return score
}
