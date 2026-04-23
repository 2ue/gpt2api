package image

// OutputCount 返回当前任务结果里的图片数量。
func (r *RunResult) OutputCount() int {
	if r == nil {
		return 0
	}
	count := len(r.SignedURLs)
	if len(r.B64JSON) > count {
		count = len(r.B64JSON)
	}
	if len(r.FileIDs) > count {
		count = len(r.FileIDs)
	}
	if len(r.Outputs) > count {
		count = len(r.Outputs)
	}
	if len(r.RevisedPrompts) > count {
		count = len(r.RevisedPrompts)
	}
	return count
}
