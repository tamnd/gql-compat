package fixture

import (
	"math"
	"math/rand/v2"
	"strconv"
)

// build expands a Generator into literal nodes and edges.
//
// Every shape draws from a seeded ChaCha8 stream, so two machines building
// the same generated fixture build the same graph edge for edge. Throughput
// numbers compared across a matrix are otherwise comparing graphs, not
// engines.
func (g *Generator) build() ([]Node, []Edge) {
	label := g.Label
	if label == "" {
		label = "N"
	}
	etype := g.EdgeType
	if etype == "" {
		etype = "E"
	}

	nodes := make([]Node, g.Nodes)
	for i := range nodes {
		n := Node{Key: strconv.Itoa(i), Labels: []string{label}}
		// The id property is always present: it is what expected-result rows
		// name, and what an engine that keeps no internal id can sort by.
		n.Props = map[string]any{"id": int64(i)}
		for p := range g.Properties {
			// Values are a function of the node so a filter's selectivity is
			// predictable: p0 splits the graph in ten, p1 in a hundred.
			n.Props["p"+strconv.Itoa(p)] = int64(i % int(math.Pow10(p+1)))
		}
		nodes[i] = n
	}

	var edges []Edge
	add := func(from, to int) {
		edges = append(edges, Edge{
			Type: etype,
			From: strconv.Itoa(from),
			To:   strconv.Itoa(to),
		})
	}

	rng := rand.New(rand.NewChaCha8(seedBytes(g.Seed)))

	switch g.Shape {
	case "path":
		for i := 0; i+1 < g.Nodes; i++ {
			add(i, i+1)
		}
	case "cycle":
		for i := range g.Nodes {
			add(i, (i+1)%g.Nodes)
		}
	case "star":
		for i := 1; i < g.Nodes; i++ {
			add(0, i)
		}
	case "grid":
		// A square-ish lattice: the shape where a k-hop neighbourhood grows
		// polynomially rather than exponentially, which separates engines
		// that cache adjacency from engines that re-read it.
		side := int(math.Ceil(math.Sqrt(float64(g.Nodes))))
		for i := range g.Nodes {
			r, c := i/side, i%side
			if c+1 < side && i+1 < g.Nodes {
				add(i, i+1)
			}
			if (r+1)*side+c < g.Nodes {
				add(i, (r+1)*side+c)
			}
		}
	case "diamonds":
		// A chain of k diamonds: every node either splits in two or joins two
		// back. Node 3i is the ith junction, 3i+1 and 3i+2 the two ways out of
		// it, and 3(i+1) the junction they meet at again.
		//
		// The point is that the answer is known before any engine sees the
		// graph. Every path from the first junction to the last is 2k edges
		// long, so all of them are shortest paths, and there are exactly 2^k of
		// them: an answer that grows exponentially in a graph that grows
		// linearly. A shortest-path query over it can be checked rather than
		// merely timed, which is what the lattice case could not offer.
		for i := 0; i+3 < g.Nodes; i += 3 {
			add(i, i+1)
			add(i, i+2)
			add(i+1, i+3)
			add(i+2, i+3)
		}
	case "clique":
		for i := range g.Nodes {
			for j := range g.Nodes {
				if i != j {
					add(i, j)
				}
			}
		}
	case "erdos-renyi":
		// Fixed mean degree rather than fixed probability, so that node count
		// and edge count scale together and bits-per-edge stays comparable.
		deg := g.Degree
		if deg <= 0 {
			deg = 8
		}
		total := g.Nodes * deg
		for range total {
			s, d := rng.IntN(g.Nodes), rng.IntN(g.Nodes)
			if s != d {
				add(s, d)
			}
		}
	case "power-law":
		// Preferential attachment. Real social graphs are heavy-tailed, and a
		// heavy tail is what makes a mean out-degree a bad predictor of the
		// work a single point lookup does; the cardinality tests want a graph
		// where that gap exists.
		deg := g.Degree
		if deg <= 0 {
			deg = 4
		}
		targets := make([]int, 0, g.Nodes*deg*2)
		for i := range min(deg+1, g.Nodes) {
			for j := range min(deg+1, g.Nodes) {
				if i != j {
					add(i, j)
					targets = append(targets, i, j)
				}
			}
		}
		for i := deg + 1; i < g.Nodes; i++ {
			// The set deduplicates; the slice fixes the order. Emitting
			// straight out of the map would make the edge list — and, because
			// targets feeds the next draw, every subsequent vertex — depend on
			// Go's randomised map iteration, and the seed would buy nothing.
			picked := map[int]bool{}
			order := make([]int, 0, deg)
			for len(picked) < deg && len(targets) > 0 {
				t := targets[rng.IntN(len(targets))]
				if t != i && !picked[t] {
					picked[t] = true
					order = append(order, t)
				}
			}
			for _, t := range order {
				add(i, t)
				targets = append(targets, i, t)
			}
		}
	}
	return nodes, edges
}

// seedBytes stretches an int64 seed into the 32 bytes ChaCha8 wants, by
// repeating it. It only has to be deterministic and to differ between seeds.
func seedBytes(seed int64) [32]byte {
	var b [32]byte
	u := uint64(seed)
	for i := range 4 {
		for j := range 8 {
			b[i*8+j] = byte(u >> (8 * j))
		}
		u = u*6364136223846793005 + 1442695040888963407
	}
	return b
}
