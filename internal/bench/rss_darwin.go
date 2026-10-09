package bench

func rssBytes(maxRSS int64) uint64 {
	return uint64(max(0, maxRSS))
}
