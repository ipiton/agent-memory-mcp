// Package topk provides a generic min-heap for top-K selection.
package topk

import "container/heap"

// MinHeap is a generic min-heap ordered by a caller-supplied comparator.
// The root element is the minimum, allowing efficient replacement during top-K collection.
type MinHeap[T any] struct {
	items []T
	less  func(a, b T) bool
}

// NewMinHeap creates a min-heap with the given capacity and comparator.
// less(a, b) should return true when a is smaller (lower priority) than b.
func NewMinHeap[T any](capacity int, less func(a, b T) bool) *MinHeap[T] {
	return &MinHeap[T]{
		items: make([]T, 0, capacity),
		less:  less,
	}
}

func (h MinHeap[T]) Len() int           { return len(h.items) }
func (h MinHeap[T]) Less(i, j int) bool { return h.less(h.items[i], h.items[j]) }
func (h MinHeap[T]) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }

func (h *MinHeap[T]) Push(x any) {
	h.items = append(h.items, x.(T))
}

func (h *MinHeap[T]) Pop() any {
	old := h.items
	n := len(old)
	item := old[n-1]
	var zero T
	old[n-1] = zero
	h.items = old[:n-1]
	return item
}

// PushItem adds an item to the heap.
func (h *MinHeap[T]) PushItem(item T) {
	heap.Push(h, item)
}

// PopItem removes and returns the minimum item.
func (h *MinHeap[T]) PopItem() T {
	return heap.Pop(h).(T)
}

// PeekMin returns the minimum item without removing it.
// Panics if the heap is empty.
func (h *MinHeap[T]) PeekMin() T {
	return h.items[0]
}

// ReplaceMin replaces the minimum item and re-heapifies.
func (h *MinHeap[T]) ReplaceMin(item T) {
	h.items[0] = item
	heap.Fix(h, 0)
}
