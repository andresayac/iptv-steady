package catalog

import "sort"

type unionFind []int

func newUnionFind(size int) unionFind {
	links := make(unionFind, size)
	for i := range links {
		links[i] = i
	}
	return links
}

func (u unionFind) find(x int) int {
	for u[x] != x {
		u[x] = u[u[x]]
		x = u[x]
	}
	return x
}

func (u unionFind) union(a, b int) { u[u.find(b)] = u.find(a) }

func linkEqualKeys(rows []*row, links unionFind, key func(*row) string) {
	first := map[string]int{}
	for i, r := range rows {
		k := key(r)
		if j, found := first[k]; found {
			links.union(j, i)
		} else {
			first[k] = i
		}
	}
}

// linkByTitle joins rows of one title whose years are close. Rows without a year join the most common year.
func linkByTitle(rows []*row, links unionFind, tolerance int) {
	byTitle := map[string]map[int][]int{}
	for i, r := range rows {
		if byTitle[r.key] == nil {
			byTitle[r.key] = map[int][]int{}
		}
		byTitle[r.key][r.year] = append(byTitle[r.key][r.year], i)
	}
	for _, years := range byTitle {
		dated := make([]int, 0, len(years))
		for year := range years {
			if year != 0 {
				dated = append(dated, year)
			}
		}
		sort.Ints(dated)
		for _, year := range dated {
			for _, member := range years[year][1:] {
				links.union(years[year][0], member)
			}
			for distance := 1; distance <= tolerance; distance++ {
				if earlier, found := years[year-distance]; found {
					links.union(years[year][0], earlier[0])
				}
			}
		}
		linkUndated(years, dated, links)
	}
}

func linkUndated(years map[int][]int, dated []int, links unionFind) {
	undated := years[0]
	if len(undated) == 0 {
		return
	}
	anchor, most := undated[0], 0
	for _, year := range dated {
		if len(years[year]) > most {
			anchor, most = years[year][0], len(years[year])
		}
	}
	for _, member := range undated {
		links.union(anchor, member)
	}
}

// linkByPoster joins differently named rows that share a poster, such as a translated title.
func linkByPoster(rows []*row, links unionFind, maxTitles, tolerance int) int {
	byPoster := map[string][]int{}
	for i, r := range rows {
		if r.poster != "" {
			byPoster[r.poster] = append(byPoster[r.poster], i)
		}
	}
	merges := 0
	for _, indexes := range byPoster {
		titles := map[string]bool{}
		for _, i := range indexes {
			titles[rows[i].key] = true
		}
		if len(titles) > maxTitles {
			continue
		}
		merges += linkPosterRows(rows, links, indexes, tolerance)
	}
	return merges
}

func linkPosterRows(rows []*row, links unionFind, indexes []int, tolerance int) int {
	sort.Slice(indexes, func(x, y int) bool {
		yearX, yearY := rows[indexes[x]].year, rows[indexes[y]].year
		if (yearX == 0) != (yearY == 0) {
			return yearY == 0
		}
		return yearX < yearY
	})
	merges := 0
	for i := 1; i < len(indexes); i++ {
		previous, current := rows[indexes[i-1]].year, rows[indexes[i]].year
		if previous != 0 && current != 0 && current-previous > tolerance {
			continue
		}
		if links.find(indexes[i-1]) != links.find(indexes[i]) {
			links.union(indexes[i-1], indexes[i])
			merges++
		}
	}
	return merges
}
