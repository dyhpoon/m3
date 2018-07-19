// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package logical

import (
	"fmt"
	"sort"

	"github.com/m3db/m3db/src/coordinator/block"
	"github.com/m3db/m3db/src/coordinator/executor/transform"
	"github.com/m3db/m3db/src/coordinator/parser"
)

//TODO: generalize logical functions?
const (
	// UnlessType uses all values from lhs which do not exist in rhs
	UnlessType           = "unless"
	initIndexSliceLength = 10
)

var (
	errMismatchedBounds     = fmt.Errorf("block bounds are mismatched")
	errMismatchedStepCounts = fmt.Errorf("block step counts are mismatched")
	errConflictingTags      = fmt.Errorf("block tags conflict")
)

// NewUnlessOp creates a new Unless operation
func NewUnlessOp(lNode parser.NodeID, rNode parser.NodeID, matching *VectorMatching) BaseOp {
	return BaseOp{
		OperatorType: UnlessType,
		LNode:        lNode,
		RNode:        rNode,
		Matching:     matching,
		ProcessorFn:  NewUnlessNode,
	}
}

// UnlessNode is a node for Unless operation
type UnlessNode struct {
	op         BaseOp
	controller *transform.Controller
}

// NewUnlessNode creates a new UnlessNode
func NewUnlessNode(op BaseOp, controller *transform.Controller) Processor {
	return &UnlessNode{
		op:         op,
		controller: controller,
	}
}

func addValuesAtIndeces(idxArray []int, iter block.StepIter, builder block.Builder) error {
	index := 0
	for ; iter.Next(); index++ {
		step, err := iter.Current()
		if err != nil {
			return err
		}
		values := step.Values()
		for _, idx := range idxArray {
			builder.AppendValue(index, values[idx])
		}
	}
	return nil
}

// Process processes two logical blocks, performing Unless operation on them
func (c *UnlessNode) Process(lhs, rhs block.Block) (block.Block, error) {
	lIter, err := lhs.StepIter()
	if err != nil {
		return nil, err
	}

	rIter, err := rhs.StepIter()
	if err != nil {
		return nil, err
	}

	if lIter.StepCount() != rIter.StepCount() {
		return nil, errMismatchedStepCounts
	}

	lSeriesMeta, rSeriesMeta := lIter.SeriesMeta(), rIter.SeriesMeta()
	lIds := c.exclusion(lSeriesMeta, rSeriesMeta)
	stepCount := len(lIds)
	takenMeta := make([]block.SeriesMeta, 0, stepCount)
	for _, idx := range lIds {
		takenMeta = append(takenMeta, lSeriesMeta[idx])
	}

	builder, err := c.controller.BlockBuilder(lIter.Meta(), takenMeta)
	if err != nil {
		return nil, err
	}

	if err := builder.AddCols(lIter.StepCount()); err != nil {
		return nil, err
	}

	if err := addValuesAtIndeces(lIds, lIter, builder); err != nil {
		return nil, err
	}

	return builder.Build(), nil
}

// exclusion returns slices for unique indices on the lhs which do not exist in rhs
func (c *UnlessNode) exclusion(lhs, rhs []block.SeriesMeta) []int {
	idFunction := hashFunc(c.op.Matching.On, c.op.Matching.MatchingLabels...)
	// The set of signatures for the left-hand side.
	leftSigs := make(map[uint64]int, len(lhs))
	for idx, meta := range lhs {
		leftSigs[idFunction(meta.Tags)] = idx
	}

	for _, rs := range rhs {
		// If there's no matching entry in the left-hand side Vector, add the sample.
		id := idFunction(rs.Tags)
		if _, ok := leftSigs[id]; ok {
			// Set left index to -1 as it should be excluded from the output
			leftSigs[id] = -1
		}
	}

	uniqueLeft := make([]int, 0, initIndexSliceLength)
	for _, v := range leftSigs {
		if v > -1 {
			uniqueLeft = append(uniqueLeft, v)
		}
	}
	// NB (arnikola): Since these values are inserted from ranging over a map, they
	// are not in order
	// TODO (arnikola): if this ends up being slow, insert in a sorted fashion.
	sort.Ints(uniqueLeft)
	return uniqueLeft
}