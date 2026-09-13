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

// AStar calculates a path from start to goal using the A* algorithm.
func AStar(start, goal Waypoint, graph Graph) []Waypoint {
	pq := make(PriorityQueue, 0)
	heap.Init(&pq)
	heap.Push(&pq, &Item{value: start, priority: 0})

	cameFrom := make(map[Waypoint]Waypoint)
	costSoFar := make(map[Waypoint]float32)

	cameFrom[start] = start
	costSoFar[start] = 0

	for pq.Len() > 0 {
		current := heap.Pop(&pq).(*Item).value

		if current == goal {
			break
		}

		for _, next := range graph.GetNeighbors(current) {
			// Cost is distance from current to next
			newCost := costSoFar[current] + heuristic(current, next)

			if prevCost, exists := costSoFar[next]; !exists || newCost < prevCost {
				costSoFar[next] = newCost
				priority := newCost + heuristic(next, goal)
				heap.Push(&pq, &Item{value: next, priority: priority})
				cameFrom[next] = current
			}
		}
	}

	// Reconstruct path
	if _, exists := cameFrom[goal]; !exists {
		return []Waypoint{} // No path found
	}

	path := []Waypoint{}
	current := goal
	for current != start {
		path = append(path, current)
		current = cameFrom[current]
	}
	path = append(path, start)

	// Reverse the path to correct order (start to goal)
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}
