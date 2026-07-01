package api

// paginate walks a Linear cursor-paginated connection, calling fetchPage for
// each page until PageInfo reports no more pages, then returns the concatenated
// nodes. fetchPage receives the cursor for the page to fetch ("" for the first
// page) and returns that page's nodes and its PageInfo.
//
// It owns the repeated control flow — the loop, cursor threading, and node
// accumulation — that every paginated query used to copy. Callers own only the
// per-query decode (which stays fully typed, so the GraphQL schema binding is
// still checked at compile time).
func paginate[T any](fetchPage func(cursor string) ([]T, PageInfo, error)) ([]T, error) {
	var all []T
	cursor := ""
	for {
		nodes, page, err := fetchPage(cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, nodes...)
		if !page.HasNextPage {
			break
		}
		cursor = page.EndCursor
	}
	return all, nil
}
