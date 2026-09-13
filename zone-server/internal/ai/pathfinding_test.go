package ai

import (
	"reflect"
	"testing"
)

// MockGraph is a simple graph for testing.
type MockGraph struct {
	nodes     map[Waypoint][]Waypoint
}

func (m *MockGraph) GetNeighbors(node Waypoint) []Waypoint {
	return m.nodes[node]
}

func TestAStar(t *testing.T) {
	// A basic line graph: A -> B -> C
	nodeA := Waypoint{0, 0, 0}
	nodeB := Waypoint{1, 0, 0}
	nodeC := Waypoint{2, 0, 0}

	graph := &MockGraph{
		nodes: map[Waypoint][]Waypoint{
			nodeA: {nodeB},
			nodeB: {nodeA, nodeC},
			nodeC: {nodeB},
		},
	}

	path := AStar(nodeA, nodeC, graph)
	expected := []Waypoint{nodeA, nodeB, nodeC}

	if !reflect.DeepEqual(path, expected) {
		t.Errorf("Expected path %v, got %v", expected, path)
	}

	// Test Unreachable path
	nodeD := Waypoint{10, 10, 10}
	unreachableGraph := &MockGraph{
		nodes: map[Waypoint][]Waypoint{
			nodeA: {nodeB},
			nodeB: {nodeA, nodeC},
			nodeC: {nodeB},
			nodeD: {}, // No neighbors for D
		},
	}

	path = AStar(nodeA, nodeD, unreachableGraph)
	if len(path) != 0 {
		t.Errorf("Expected empty path for unreachable node, got %v", path)
	}

	// Test Path with Obstacle (A -> B -> C vs A -> D -> C where D is shorter but blocked, A -> B -> C is faster)
    // Wait, simple mock:
	// A - B - C
	// |       |
	// D - - - E

	nodeE := Waypoint{2, 1, 0}
	nodeD2 := Waypoint{0, 1, 0} // D

	graph2 := &MockGraph{
		nodes: map[Waypoint][]Waypoint{
			nodeA: {nodeB, nodeD2},
			nodeB: {nodeA, nodeC},
			nodeC: {nodeB, nodeE},
			nodeD2: {nodeA, nodeE},
			nodeE: {nodeD2, nodeC},
		},
	}
	// Euclidean distance A to C is 2. Path A-B-C cost is 1+1 = 2
	// Path A-D-E-C cost is 1+2+1 = 4.
	// Actually D(0,1,0) to E(2,1,0) is 2.

	path = AStar(nodeA, nodeC, graph2)
	expected2 := []Waypoint{nodeA, nodeB, nodeC}
	if !reflect.DeepEqual(path, expected2) {
		t.Errorf("Expected path %v, got %v", expected2, path)
	}

	// Start == Goal
	path = AStar(nodeA, nodeA, graph)
	expectedStartGoal := []Waypoint{nodeA}
	if !reflect.DeepEqual(path, expectedStartGoal) {
		t.Errorf("Expected path %v, got %v", expectedStartGoal, path)
	}
}
