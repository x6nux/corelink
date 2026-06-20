// Package version 定义全网通用的 (epoch, generation) 配置版本号。
package version

// ConfigVersion 是 (epoch, generation) 字典序版本号。
// Epoch = 全网控制平面纪元（当前恒 0，预留扩展）；
// Generation = per-node 单调配置版本。节点只接受更大的 ConfigVersion。
type ConfigVersion struct {
	Epoch      uint64
	Generation uint64
}

func (v ConfigVersion) Compare(o ConfigVersion) int {
	switch {
	case v.Epoch != o.Epoch:
		if v.Epoch < o.Epoch {
			return -1
		}
		return 1
	case v.Generation != o.Generation:
		if v.Generation < o.Generation {
			return -1
		}
		return 1
	default:
		return 0
	}
}

func (v ConfigVersion) Less(o ConfigVersion) bool { return v.Compare(o) < 0 }
func (v ConfigVersion) IsZero() bool              { return v.Epoch == 0 && v.Generation == 0 }
