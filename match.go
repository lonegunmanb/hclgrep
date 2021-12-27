package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type substitution struct {
	String         *string
	Node           hclsyntax.Node
	ObjectConsItem *hclsyntax.ObjectConsItem
}

func newStringSubstitution(s string) substitution {
	return substitution{String: &s}
}

func newNodeSubstitution(node hclsyntax.Node) substitution {
	return substitution{Node: node}
}

func newObjectConsItemSubstitution(item *hclsyntax.ObjectConsItem) substitution {
	return substitution{ObjectConsItem: item}
}

type matcher struct {
	values map[string]substitution
}

func (m *matcher) node(pattern, node hclsyntax.Node) bool {
	if pattern == nil || node == nil {
		return pattern == node
	}
	if pattern != nil {
		switch node := node.(type) {
		case hclsyntax.Attributes:
			if len(node) == 0 {
				return false
			}
		case hclsyntax.Blocks:
			if len(node) == 0 {
				return false
			}
		}
	}
	switch x := pattern.(type) {
	// Expressions
	case *hclsyntax.LiteralValueExpr:
		y, ok := node.(*hclsyntax.LiteralValueExpr)
		return ok && x.Val.Equals(y.Val).True()
	case *hclsyntax.TupleConsExpr:
		y, ok := node.(*hclsyntax.TupleConsExpr)
		return ok && m.exprs(x.Exprs, y.Exprs)
	case *hclsyntax.ObjectConsExpr:
		y, ok := node.(*hclsyntax.ObjectConsExpr)
		return ok && m.objectConsItems(x.Items, y.Items)
	case *hclsyntax.TemplateExpr:
		y, ok := node.(*hclsyntax.TemplateExpr)
		return ok && m.exprs(x.Parts, y.Parts)
	case *hclsyntax.FunctionCallExpr:
		y, ok := node.(*hclsyntax.FunctionCallExpr)
		return ok &&
			m.potentialWildcardIdentEqual(x.Name, y.Name) &&
			m.exprs(x.Args, y.Args) && x.ExpandFinal == y.ExpandFinal
	case *hclsyntax.ForExpr:
		y, ok := node.(*hclsyntax.ForExpr)
		return ok &&
			m.potentialWildcardIdentEqual(x.KeyVar, y.KeyVar) &&
			m.potentialWildcardIdentEqual(x.ValVar, y.ValVar) &&
			m.node(x.CollExpr, y.CollExpr) && m.node(x.KeyExpr, y.KeyExpr) && m.node(x.ValExpr, y.ValExpr) && m.node(x.CondExpr, y.CondExpr) && x.Group == y.Group
	case *hclsyntax.IndexExpr:
		y, ok := node.(*hclsyntax.IndexExpr)
		return ok && m.node(x.Collection, y.Collection) && m.node(x.Key, y.Key)
	case *hclsyntax.SplatExpr:
		y, ok := node.(*hclsyntax.SplatExpr)
		return ok && m.node(x.Source, y.Source) && m.node(x.Each, y.Each) && m.node(x.Item, y.Item)
	case *hclsyntax.ParenthesesExpr:
		y, ok := node.(*hclsyntax.ParenthesesExpr)
		return ok && m.node(x.Expression, y.Expression)
	case *hclsyntax.UnaryOpExpr:
		y, ok := node.(*hclsyntax.UnaryOpExpr)
		return ok && m.operation(x.Op, y.Op) && m.node(x.Val, y.Val)
	case *hclsyntax.BinaryOpExpr:
		y, ok := node.(*hclsyntax.BinaryOpExpr)
		return ok && m.operation(x.Op, y.Op) && m.node(x.LHS, y.LHS) && m.node(x.RHS, y.RHS)
	case *hclsyntax.ConditionalExpr:
		y, ok := node.(*hclsyntax.ConditionalExpr)
		return ok && m.node(x.Condition, y.Condition) && m.node(x.TrueResult, y.TrueResult) && m.node(x.FalseResult, y.FalseResult)
	case *hclsyntax.ScopeTraversalExpr:
		xname, ok := variableExpr(x)
		if ok && isWildName(xname) {
			name, _ := fromWildName(xname)
			return m.wildcardMatchNode(name, node)
		}
		y, ok := node.(*hclsyntax.ScopeTraversalExpr)
		return ok && m.traversal(x.Traversal, y.Traversal)
	case *hclsyntax.RelativeTraversalExpr:
		y, ok := node.(*hclsyntax.RelativeTraversalExpr)
		return ok && m.traversal(x.Traversal, y.Traversal) && m.node(x.Source, y.Source)
	case *hclsyntax.ObjectConsKeyExpr:
		y, ok := node.(*hclsyntax.ObjectConsKeyExpr)
		return ok && m.node(x.Wrapped, y.Wrapped) && x.ForceNonLiteral == y.ForceNonLiteral
	case *hclsyntax.TemplateJoinExpr:
		y, ok := node.(*hclsyntax.TemplateJoinExpr)
		return ok && m.node(x.Tuple, y.Tuple)
	case *hclsyntax.TemplateWrapExpr:
		y, ok := node.(*hclsyntax.TemplateWrapExpr)
		return ok && m.node(x.Wrapped, y.Wrapped)
	case *hclsyntax.AnonSymbolExpr:
		_, ok := node.(*hclsyntax.AnonSymbolExpr)
		// Only do type check
		return ok
	// Body
	case *hclsyntax.Body:
		y, ok := node.(*hclsyntax.Body)
		return ok && m.body(x, y)
	// Attribute
	case *hclsyntax.Attribute:
		return m.attribute(x, node)
	// Block
	case *hclsyntax.Block:
		y, ok := node.(*hclsyntax.Block)
		return ok && m.block(x, y)
	default:
		// Including:
		// - hclsyntax.ChildScope
		// - hclsyntax.Blocks
		// - hclsyntax.Attributes
		panic(fmt.Sprintf("unexpected node: %T", x))
	}
}

type matchFunc func(*matcher, interface{}, interface{}) bool
type wildNameFunc func(interface{}) (string, bool)

// iterableMatches matches two lists. It uses a common algorithm to match
// wildcard patterns with any number of elements without recursion.
func (m *matcher) iterableMatches(ns1, ns2 []interface{}, nf wildNameFunc, mf matchFunc) bool {
	i1, i2 := 0, 0
	next1, next2 := 0, 0
	for i1 < len(ns1) || i2 < len(ns2) {
		if i1 < len(ns1) {
			n1 := ns1[i1]
			if _, any := nf(n1); any {
				// try to match zero or more at i2,
				// restarting at i2+1 if it fails
				next1 = i1
				next2 = i2 + 1
				i1++
				continue
			}
			if i2 < len(ns2) && mf(m, n1, ns2[i2]) {
				// ordinary match
				i1++
				i2++
				continue
			}
		}
		// mismatch, try to restart
		if 0 < next2 && next2 <= len(ns2) {
			i1 = next1
			i2 = next2
			continue
		}
		return false
	}
	return true
}

// Node comparisons

func wildNameFromNode(in interface{}) (string, bool) {
	switch node := in.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		name, ok := variableExpr(node)
		if !ok {
			return "", false
		}
		return fromWildName(name)
	case *hclsyntax.Attribute:
		return fromWildName(node.Name)
	default:
		return "", false
	}
}

func matchNode(m *matcher, x, y interface{}) bool {
	nx, ny := x.(hclsyntax.Node), y.(hclsyntax.Node)
	return m.node(nx, ny)
}

func (m *matcher) attribute(x *hclsyntax.Attribute, y hclsyntax.Node) bool {
	if x == nil || y == nil {
		return x == y
	}
	if isWildAttr(x.Name, x.Expr) {
		// The wildcard attribute can only match attribute or block
		switch y := y.(type) {
		case *hclsyntax.Attribute,
			*hclsyntax.Block:
			name, _ := fromWildName(x.Name)
			return m.wildcardMatchNode(name, y)
		default:
			return false
		}
	}
	attrY, ok := y.(*hclsyntax.Attribute)
	return ok && m.node(x.Expr, attrY.Expr) &&
		m.potentialWildcardIdentEqual(x.Name, attrY.Name)
}

func (m *matcher) block(x, y *hclsyntax.Block) bool {
	if x == nil || y == nil {
		return x == y
	}
	return m.potentialWildcardIdentEqual(x.Type, y.Type) &&
		m.potentialWildcardIdentsEqual(x.Labels, y.Labels) &&
		m.body(x.Body, y.Body)
}

func (m *matcher) body(x, y *hclsyntax.Body) bool {
	if x == nil || y == nil {
		return x == y
	}

	// Sort the attributes/blocks to reserve the order in source
	bodyEltsX := sortBody(x)
	bodyEltsY := sortBody(y)

	ns1 := make([]interface{}, len(bodyEltsX))
	for i, n := range bodyEltsX {
		ns1[i] = n
	}
	ns2 := make([]interface{}, len(bodyEltsY))
	for i, n := range bodyEltsY {
		ns2[i] = n
	}
	return m.iterableMatches(ns1, ns2, wildNameFromNode, matchNode)
}

func (m *matcher) exprs(exprs1, exprs2 []hclsyntax.Expression) bool {
	ns1 := make([]interface{}, len(exprs1))
	for i, n := range exprs1 {
		ns1[i] = n
	}
	ns2 := make([]interface{}, len(exprs2))
	for i, n := range exprs2 {
		ns2[i] = n
	}
	return m.iterableMatches(ns1, ns2, wildNameFromNode, matchNode)
}

// Operation comparisons

func (m *matcher) operation(op1, op2 *hclsyntax.Operation) bool {
	if op1 == nil || op2 == nil {
		return op1 == op2
	}
	return op1.Impl == op2.Impl && op1.Type.Equals(op2.Type)
}

// ObjectConsItems comparisons

func wildNameFromObjectConsItem(in interface{}) (string, bool) {
	switch node := in.(hclsyntax.ObjectConsItem).KeyExpr.(type) {
	case *hclsyntax.ObjectConsKeyExpr:
		name, ok := variableExpr(node.Wrapped)
		if !ok {
			return "", false
		}
		return fromWildName(name)
	default:
		return "", false
	}
}

func matchObjectConsItem(m *matcher, x, y interface{}) bool {
	itemX, itemY := x.(hclsyntax.ObjectConsItem), y.(hclsyntax.ObjectConsItem)
	return m.objectConsItem(itemX, itemY)
}

func (m *matcher) objectConsItems(items1, items2 []hclsyntax.ObjectConsItem) bool {
	its1 := make([]interface{}, len(items1))
	for i, n := range items1 {
		its1[i] = n
	}
	its2 := make([]interface{}, len(items2))
	for i, n := range items2 {
		its2[i] = n
	}

	return m.iterableMatches(its1, its2, wildNameFromObjectConsItem, matchObjectConsItem)
}

func (m *matcher) objectConsItem(item1, item2 hclsyntax.ObjectConsItem) bool {
	name, ok := variableExpr(item1.KeyExpr)
	if ok && isWildAttr(name, item1.ValueExpr) {
		return m.wildcardMatchObjectConsItem(name, item2)
	}
	return m.node(item1.KeyExpr, item2.KeyExpr) && m.node(item1.ValueExpr, item2.ValueExpr)
}

// String comparisons

func wildNameFromString(in interface{}) (string, bool) {
	return fromWildName(in.(string))
}

func matchString(m *matcher, x, y interface{}) bool {
	sx, sy := x.(string), y.(string)
	return m.potentialWildcardIdentEqual(sx, sy)
}

func (m *matcher) potentialWildcardIdentsEqual(identX, identY []string) bool {
	ss1 := make([]interface{}, len(identX))
	for i, n := range identX {
		ss1[i] = n
	}
	ss2 := make([]interface{}, len(identY))
	for i, n := range identY {
		ss2[i] = n
	}

	return m.iterableMatches(ss1, ss2, wildNameFromString, matchString)
}

func (m *matcher) potentialWildcardIdentEqual(identX, identY string) bool {
	if !isWildName(identX) {
		return identX == identY
	}
	name, _ := fromWildName(identX)
	return m.wildcardMatchString(name, identY)
}

// Traversal comparisons

func (m *matcher) traversal(traversal1, traversal2 hcl.Traversal) bool {
	if len(traversal1) != len(traversal2) {
		return false
	}
	for i, t1 := range traversal1 {
		if !m.traverser(t1, traversal2[i]) {
			return false
		}
	}
	return true
}

func (m *matcher) traverser(t1, t2 hcl.Traverser) bool {
	switch t1 := t1.(type) {
	case hcl.TraverseRoot:
		t2, ok := t2.(hcl.TraverseRoot)
		return ok && m.potentialWildcardIdentEqual(t1.Name, t2.Name)
	case hcl.TraverseAttr:
		t2, ok := t2.(hcl.TraverseAttr)
		return ok && m.potentialWildcardIdentEqual(t1.Name, t2.Name)
	case hcl.TraverseIndex:
		t2, ok := t2.(hcl.TraverseIndex)
		return ok && t1.Key.Equals(t2.Key).True()
	case hcl.TraverseSplat:
		t2, ok := t2.(hcl.TraverseSplat)
		return ok && m.traversal(t1.Each, t2.Each)
	default:
		panic(fmt.Sprintf("unexpected node: %T", t1))
	}
}

func (m *matcher) wildcardMatchNode(name string, node hclsyntax.Node) bool {
	if name == "_" {
		// values are discarded, matches anything
		return true
	}
	prev, ok := m.values[name]
	if !ok {
		m.values[name] = newNodeSubstitution(node)
		return true
	}
	switch {
	case prev.String != nil:
		nodeVar, ok := variableExpr(node)
		return ok && nodeVar == *prev.String
	case prev.Node != nil:
		return m.node(prev.Node, node)
	case prev.ObjectConsItem != nil:
		return false
	default:
		panic("never reach here")
	}
}

func (m *matcher) wildcardMatchString(name, target string) bool {
	if name == "_" {
		// values are discarded, matches anything
		return true
	}
	prev, ok := m.values[name]
	if !ok {
		m.values[name] = newStringSubstitution(target)
		return true
	}

	switch {
	case prev.String != nil:
		return *prev.String == target
	case prev.Node != nil:
		prevName, ok := variableExpr(prev.Node)
		return ok && prevName == target
	case prev.ObjectConsItem != nil:
		return false
	default:
		panic("never reach here")
	}
}

func (m *matcher) wildcardMatchObjectConsItem(name string, item hclsyntax.ObjectConsItem) bool {
	if name == "_" {
		// values are discarded, matches anything
		return true
	}
	prev, ok := m.values[name]
	if !ok {
		m.values[name] = newObjectConsItemSubstitution(&item)
		return true
	}
	switch {
	case prev.String != nil:
		return false
	case prev.Node != nil:
		return false
	case prev.ObjectConsItem != nil:
		return m.objectConsItem(*prev.ObjectConsItem, item)
	default:
		panic("never reach here")
	}
}

// Two wildcard: expression wildcard ($) and attribute wildcard (@)
// - expression wildcard: $<ident> => hclgrep_<ident>
// - expression wildcard (any): $<ident> => hclgrep_any_<ident>
// - attribute wildcard : @<ident> => hclgrep-<index>_<ident> = hclgrepattr
// - attribute wildcard (any) : @<ident> => hclgrep_any-<index>_<ident> = hclgrepattr
const (
	wildPrefix    = "hclgrep_"
	wildExtraAny  = "any_"
	wildAttrValue = "hclgrepattr"
)

var wildattrCounters = map[string]int{}

func wildName(name string, any bool) string {
	prefix := wildPrefix
	if any {
		prefix += wildExtraAny
	}
	return prefix + name
}

func wildAttr(name string, any bool) string {
	attr := wildName(name, any) + "-" + strconv.Itoa(wildattrCounters[name]) + "=" + wildAttrValue
	wildattrCounters[name] += 1
	return attr
}

func isWildName(name string) bool {
	return strings.HasPrefix(name, wildPrefix)
}

func isWildAttr(key string, value hclsyntax.Expression) bool {
	v, ok := variableExpr(value)
	return ok && v == wildAttrValue && isWildName(key)
}

func fromWildName(name string) (ident string, any bool) {
	ident = strings.TrimPrefix(strings.Split(name, "-")[0], wildPrefix)
	return strings.TrimPrefix(ident, wildExtraAny), strings.HasPrefix(ident, wildExtraAny)
}

func variableExpr(node hclsyntax.Node) (string, bool) {
	if _, ok := node.(*hclsyntax.ObjectConsKeyExpr); ok {
		node = node.(*hclsyntax.ObjectConsKeyExpr).Wrapped
	}
	vexp, ok := node.(*hclsyntax.ScopeTraversalExpr)
	if !(ok && len(vexp.Traversal) == 1 && !vexp.Traversal.IsRelative()) {
		return "", false
	}
	return vexp.Traversal.RootName(), true
}

func sortBody(body *hclsyntax.Body) []hclsyntax.Node {
	l := len(body.Blocks) + len(body.Attributes)
	m := make(map[int]hclsyntax.Node, l)
	offsets := make([]int, 0, l)
	for _, blk := range body.Blocks {
		offset := blk.Range().Start.Byte
		m[offset] = blk
		offsets = append(offsets, offset)
	}
	for _, attr := range body.Attributes {
		offset := attr.Range().Start.Byte
		m[offset] = attr
		offsets = append(offsets, offset)
	}
	sort.Ints(offsets)
	out := make([]hclsyntax.Node, 0, l)
	for _, offset := range offsets {
		out = append(out, m[offset])
	}
	return out
}
