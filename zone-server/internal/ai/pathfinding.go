package ai

import (
	"container/heap"
	"math"
)

type Waypoint struct {
	X, Y, Z float32
}

// Graph defines the interface for evaluating neighboring waypoints.
type Graph interface {
	GetNeighbors(node Waypoint) []Waypoint
}

// Item represents a node in the priority queue.
type Item struct {
	value    Waypoint
	priority float32
	index    int
}

// PriorityQueue implements heap.Interface and holds Items.
type PriorityQueue []*Item

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].priority < pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*Item)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1
	*pq = old[0 : n-1]
	return item
}

// heuristic calculates the Euclidean distance between two waypoints.
func heuristic(a, b Waypoint) float32 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// PointKey is a quantized [3]int32 coordinate key used for robust map indexing
// without fragile floating-point struct equality.
type PointKey [3]int32

// QuantizeWaypoint converts a Waypoint with float32 coordinates into a PointKey
// using millimeter-level precision (scale factor 1000).
func QuantizeWaypoint(w Waypoint) PointKey {
	const scale = 1000.0
	return PointKey{
		int32(math.Round(float64(w.X * scale))),
		int32(math.Round(float64(w.Y * scale))),
		int32(math.Round(float64(w.Z * scale))),
	}
}

const MaxIterations = 5000

// AStar calculates a path from start to goal using the A* algorithm.
func AStar(start, goal Waypoint, graph Graph) []Waypoint {
	startKey := QuantizeWaypoint(start)
	goalKey := QuantizeWaypoint(goal)

	pq := make(PriorityQueue, 0)
	heap.Init(&pq)
	heap.Push(&pq, &Item{value: start, priority: 0})

	cameFrom := make(map[PointKey]Waypoint)
	waypointMap := make(map[PointKey]Waypoint)
	gScore := make(map[PointKey]float32)
	fScore := make(map[PointKey]float32)
	openSet := make(map[PointKey]bool)

	cameFrom[startKey] = start
	waypointMap[startKey] = start
	gScore[startKey] = 0
	fScore[startKey] = heuristic(start, goal)
	openSet[startKey] = true

	iterations := 0
	for pq.Len() > 0 {
		iterations++
		if iterations > MaxIterations {
			return nil // iteration limit exceeded on pathological graphs
		}

		current := heap.Pop(&pq).(*Item).value
		currentKey := QuantizeWaypoint(current)
		delete(openSet, currentKey)

		if currentKey == goalKey {
			break
		}

		for _, next := range graph.GetNeighbors(current) {
			nextKey := QuantizeWaypoint(next)
			tentativeGScore := gScore[currentKey] + heuristic(current, next)

			prevG, exists := gScore[nextKey]
			if !exists || tentativeGScore < prevG {
				cameFrom[nextKey] = current
				waypointMap[nextKey] = next
				gScore[nextKey] = tentativeGScore
				h := heuristic(next, goal)
				f := tentativeGScore + h
				fScore[nextKey] = f

				heap.Push(&pq, &Item{value: next, priority: f})
				openSet[nextKey] = true
			}
		}
	}

	// Reconstruct path
	if _, exists := cameFrom[goalKey]; !exists {
		return []Waypoint{} // No path found
	}

	path := []Waypoint{}
	currKey := goalKey
	for currKey != startKey {
		path = append(path, waypointMap[currKey])
		pred := cameFrom[currKey]
		currKey = QuantizeWaypoint(pred)
	}
	path = append(path, start)

	// Reverse the path to correct order (start to goal)
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}

// FindPath calculates a path from start to goal using the A* algorithm with iteration limit safety.
func FindPath(start, goal Waypoint, graph Graph) []Waypoint {
	return AStar(start, goal, graph)
}
