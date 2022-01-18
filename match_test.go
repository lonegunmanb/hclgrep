package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"

	"github.com/hashicorp/hcl/v2"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type wantErr string

func tokErr(msg string) wantErr {
	return wantErr("cannot tokenize expr: " + msg)
}

func parseErr(msg string) wantErr {
	return wantErr("cannot parse expr: " + msg)
}

func TestMatch(t *testing.T) {
	tests := []struct {
		expr, src string
		count     interface{}
	}{
		// literal expression
		{"1", "1", 1},
		{"true", "false", 0},

		// literal expression (wildcard)
		{"x = $_", "x = 1", 1},
		{"x = $_", "x = false", 1},
		{"x = $*_", "x = false", 1},

		// tuple cons expression
		{"[1, 2]", "[1, 3]", 0},
		{"[1, 2]", "[1, 2]", 1},

		// tuple cons expression (wildcard)
		{"x = $_", "x = [1, 2, 3]", 1},
		{"[1, $_, 3]", "[1, 2, 3]", 1},
		{"[1, $_, 3]", "[1, 3]", 0},
		{"[1, $x, $x]", "[1, 2, 2]", 1},
		{"[1, $x, $x]", "[1, 2, 3]", 0},
		{
			expr: `
[
	$x,
	1,
	$x,
]`,
			src: `
[
	2,
	1,
	2,
]`,
			count: 1,
		},
		{
			expr: `
[
	$x,
	1,
	$x,
]`,
			src:   `[2, 1, 2]`,
			count: 1,
		},
		{"[1, $*_]", "[1, 2, 3]", 1},
		{"[$*_, 1]", "[1, 2, 3]", 0},
		{"[$*_]", "[]", 1},
		{"[$*_, $x]", "[1, 2, 3]", 1},

		// object const expression
		{"{a = b}", "{a = b}", 1},
		{"{a = c}", "{a = b}", 0},
		{
			expr: `
		{
			a = b
			c = d
		}`,
			src: `
		{
			a = b
			c = d
		}`,
			count: 1,
		},

		// object const expression (wildcard)
		{"x = $_", "x = {a = b}", 1},
		{"{$x = $x}", "{a = a}", 1},
		{"{$x = $x}", "{a = b}", 0},
		{
			expr: `
		{
			a = $x
			c = $x
		}`,
			src: `
		{
			a = b
			c = b
		}`,
			count: 1,
		},
		{
			expr: `
		{
			a = $x
			c = $x
		}`,
			src: `
		{
			a = b
			c = d
		}`,
			count: 0,
		},
		{
			expr: `
		{
			$_ = $_
			$_ = $_
		}`,
			src: `
		{
			a = b
			c = d
		}`,
			count: 1,
		},
		{
			expr: `
		{
			@_
			@_
		}`,
			src: `
		{
			a = b
			c = d
		}`,
			count: 1,
		},
		{
			expr: `
		{
			@*_
		}`,
			src: `
		{
			a = b
			c = d
		}`,
			count: 1,
		},
		{
			expr: `
		{
			@*_
			e = f
		}`,
			src: `
		{
			a = b
			c = d
			e = f
		}`,
			count: 1,
		},

		// template expression
		{`"a"`, `"a"`, 1},
		{`"a"`, `"b"`, 0},
		{
			expr: `<<EOF
content
EOF
`,
			src: `<<EOF
content
EOF
`,
			count: 1,
		},
		{
			expr: `<<EOF
content
EOF
`,
			src: `<<EOF
other content
EOF
`,
			count: 0,
		},

		// template expression (wildcard)
		{`x= $_`, `x = "a"`, 1},
		{
			expr: "x = $_",
			src: `x = <<EOF
content
EOF
`,
			count: 1,
		},

		// function call expression
		{"f1()", "f1()", 1},
		{"f1()", "f2()", 0},
		{"f1()", "f1(arg)", 0},

		// function call expression (wildcard)
		{"x = $_", "x = f1()", 1},
		{"$_()", "f1()", 1},
		{"$_()", "f1(arg)", 0},
		{"f1($_)", "f1(arg)", 1},
		{"$_($_)", "f1(arg)", 1},
		{"f1($x, $x)", "f1(arg, arg)", 1},
		{"f1($x, $x)", "f1(arg, arg2)", 0},
		{"f1($*_)", "f1(arg, arg2)", 1},
		{"f1($*_, arg1)", "f1(arg, arg2)", 0},

		// for expression
		{"[for i in list: i]", "[for i in list: i]", 1},
		{"[for i in list: i]", "[for i in list: upper(i)]", 0},
		{"{for k, v in map: k => v}", "{for k, v in map: k => upper(v)}", 0},
		{"{for k, v in map: k => upper(v)}", "{for k, v in map: k => upper(v)}", 1},

		// for expression (wildcard)
		{"x = $_", "x = {for k, v in map: k => upper(v)}", 1},
		{"{for k, v in map: $k => upper($v)}", "{for k, v in map: k => upper(v)}", 1},
		{"{for $k, $v in map: $k => upper($v)}", "{for k, v in map: k => upper(v)}", 1},

		// index expression
		{"foo[a]", "foo[a]", 1},
		{"foo[a]", "foo[b]", 0},

		// index expression (wildcard)
		{"x = $_", "x = foo[a]", 1},
		{"foo[$x]", "foo[a]", 1},
		{"foo[$*x]", "foo[a]", 1},
		{"a[$x]", "a[1]", 1},
		{"foo()[$x]", "foo()[1]", 1},
		{"[1,2,3][$x]", "[1,2,3][1]", 1},
		{`"abc"[$x]`, `"abc"[0]`, 1},
		{`x[0][$x]`, `x[0][0]`, 1},
		{`x[$x][$x]`, `x[0][0]`, 1},
		{`x[$x][$x]`, `x[0][1]`, 0},

		// splat expression
		{"tuple.*.foo.bar[0]", "tuple.*.foo.bar[0]", 1},
		{"tuple.*.foo.bar[0]", "tuple.*.bar.bar[0]", 0},
		{"tuple[*].foo.bar[0]", "tuple[*].foo.bar[0]", 1},
		{"tuple[*].foo.bar[0]", "tuple[*].bar.bar[0]", 0},

		// splat expression (wildcard)
		{"x = $_", "x = tuple.*.foo.bar[0]", 1},
		{"x = $_", "x = tuple[*].foo.bar[0]", 1},
		{"x = $*_", "x = tuple[*].foo.bar[0]", 1},

		// parenthese expression
		{"(a)", "(a)", 1},
		{"(a)", "(b)", 0},

		// parenthese expression (wildcard)
		{"x = $_", "x = (a)", 1},
		{"($_)", "(b)", 1},
		{"($*_)", "(b)", 1},

		// unary operation expression
		{"-1", "-1", 1},
		{"-1", "1", 0},

		// unary operation expression (wildcard)
		{"x = $_", "x = -1", 1},
		{"x = $_", "x = !true", 1},
		{"x = $*_", "x = !true", 1},

		// binary operation expression
		{"1+1", "1+1", 1},
		{"1+1", "1-1", 0},

		// binary operation expression (wildcard)
		{"x = $_", "x = 1+1", 1},
		{"x = $*_", "x = 1+1", 1},

		// conditional expression
		{"cond? 0:1", "cond? 0:1", 1},
		{"cond? 0:1", "cond? 1:0", 0},

		// conditional expression (wildcard)
		{"x = $_", "x = cond? 0:1", 1},
		{"$_? 0:1", "cond? 0:1", 1},
		{"cond? 0:$_", "cond? 0:1", 1},
		{"cond? 0:$*_", "cond? 0:1", 1},

		// scope traversal expression
		{"a", "a", 1},
		{"a", "b", 0},
		{"a.attr", "a.attr", 1},
		{"a.attr", "a.attr2", 0},
		{"a[0]", "a[0]", 1},
		{"a[0]", "a[1]", 0},
		{"a.0", "a.0", 1},
		{"a.0", "a[0]", 1}, //index or legacy index are considered the same
		{"a.0", "a.1", 0},

		// scope traversal expression (wildcard)
		{"x = $_", "x = a", 1},
		{"x = $_", "x = a.attr", 1},
		{"x = $_", "x = a[0]", 1},
		{"x = $_", "x = a.0", 1},
		{"x = $_", "x = a.x.y.x", 1},
		{"$_.$_", "a.x.y.x", 0},
		{"a.$_.$_.$_", "a.x.y.z", 1},
		{"a.$x.$_.$x", "a.x.y.z", 0},
		{"a.$x.$_.$x", "a.x.y.x", 1},
		{"$_.$x.$_.$x", "a.x.y.x", 1},
		{"a.$x.$*_.$x", "a.x.y.z", 0},

		// relative traversal expression
		{"sort()[0]", "sort()[0]", 1},
		{"sort()[0]", "sort()[1]", 0},
		{"sort()[0]", "reverse()[0]", 0},

		// relative traversal expression (wildcard)
		{"x = $_", "x = sort()[0]", 1},
		{"$_()[0]", "sort()[0]", 1},
		{"$_()[0]", "sort(arg)[0]", 0},
		{"$*_()[0]", "sort(arg)[0]", 0},

		// TODO: object cons key expression
		// TODO: template join expression
		// TODO: template wrap expression
		// TODO: anonym symbol expression

		// attribute
		{"a = a", "a = a", 1},
		{"a = a", "a = b", 0},

		// attribute (wildcard)
		{"$x = $x", "a = a", 1},
		{"$x = $x", "a = b", 0},
		{"$x = $*_", "a = b", 1},

		// attributes
		{
			expr: `
a = b
c = d
`,
			src: `
a = b
c = d
`,
			count: 1,
		},
		{
			expr: `
a = b
c = d
`,
			src: `
a = b
`,
			count: 0,
		},

		// attributes (wildcard)
		{
			expr: `
@x
@y
`,
			src: `
a = b
c = d
`,
			count: 1,
		},
		{
			expr: `
a = $x
c = $x
`,
			src: `
a = b
c = d
`,
			count: 0,
		},
		{
			expr: `
a = $x
c = $x
`,
			src: `
a = b
c = b
`,
			count: 1,
		},
		{
			expr: `
a = $x
c = $x
`,
			src: `
a = b
c = b
`,
			count: 1,
		},
		{
			expr: `@*_`,
			src: `
a = b
c = d
`,
			count: 2,
		},
		{
			expr: `
@*_
e = f
`,
			src: `
a = b
c = d
e = f
`,
			count: 1,
		},

		// block
		{
			expr: `blk {
	a = b
}`,
			src: `blk {
	a = b
}`,
			count: 1,
		},
		{
			expr: `blk {
	a = b
	c = d
}`,
			src: `blk {
	a = b
}`,
			count: 0,
		},

		// block (wildcard)
		{
			expr: `$_ {
    a = b
}`,
			src: `blk {
	a = b
}`,
			count: 1,
		},
		{
			expr: `blk {
	a = $x
	c = $x
}`,
			src: `blk {
	a = b
	c = d
}`,
			count: 0,
		},
		{
			expr: `blk {
	a = $x
	c = $x
}`,
			src: `blk {
	a = b
	c = b
}`,
			count: 1,
		},
		{
			expr: `$a {
	a = $x
	b = ""
}`,
			src: `
blk1 {
	blk2 {
		a = file("./a.txt")
		b = ""
	}
}
`,
			count: 1,
		},
		{
			expr: `$*_ {
	a = b
}`,
			src: `type label1 label2 {
	a = b
}`,
			count: 0,
		},
		{
			expr: `type $*_ {
	a = b
}`,
			src: `type label1 label2 {
	a = b
}`,
			count: 1,
		},

		// blocks
		{
			expr: `blk1 {
	a = b
}

blk2 {
    c = d
}`,
			src: `blk1 {
	a = b
}

blk2 {
    c = d
}`,
			count: 1,
		},
		{
			expr: `blk1 {
	a = b
}

blk2 {
    c = d
}`,
			src: `blk1 {
	a = b
}`,
			count: 0,
		},

		// blocks (wildcard)
		{
			expr: `
$x {
	a = b
}

$x {
    c = d
}`,
			src: `
blk1 {
	a = b
}

blk2 {
    c = d
}`,
			count: 0,
		},
		{
			expr: `
$x {
	a = b
}

$x {
    c = d
}`,
			src: `
blk1 {
	a = b
}

blk1 {
    c = d
}`,
			count: 1,
		},
		{
			expr: `
@*_

$x {
    c = d
}`,
			src: `
blk1 {}
blk1 {}

blk1 {
    c = d
}`,
			count: 1,
		},
		{
			expr: `$_`,
			src: `
blk1 {}
blk1 {}`,
			count: 5, // 1 toplevel body + 2* (1 body + 1 block)
		},

		// body
		{
			expr: `
a = 1
block {
  b = 2
}
`,
			src: `
a = 1
block {
  b = 2
}
`,
			count: 1,
		},
		{
			expr: `
a = 1
block {
  b = 2
}
`,
			src: `
a = 1
`,
			count: 0,
		},

		// body (wildcard)
		{
			expr: `blk {
  @_
  @_
}`,
			src: `
blk {
	a = 1
	block {
	  b = 2
	}
}
`,
			count: 1,
		},
		{
			expr: `@x`,
			src: `
blk {
	a = 1
	block {
	  b = 2
	}
}
`,
			count: 4,
		},
		{
			expr: `
blk {
  $_ {}
}
`,
			src: `
blk {
  blk1 {}
}
`,
			count: 1,
		},
		{
			expr: `
@_

blk {
 @_
}
`,
			src: `
a = b

blk {
 blk1 {}
}
`,
			count: 1,
		},
		{
			expr: `
@x

blk {
 @x
}
`,
			src: `
a = b

blk {
 a = b
}
`,
			count: 1,
		},
		{
			expr: `
@x

blk {
 @x
}
`,
			src: `
a = b

blk {
 a = c
}
`,
			count: 0,
		},
		{
			expr: `
@x

blk {
 @x
}
`,
			src: `
blk1 {}

blk {
 blk1 {}
}
`,
			count: 1,
		},
		{
			expr: `
@x

blk {
 @x
}
`,
			src: `
a = b

blk {
 blk1 {}
}
`,
			count: 0,
		},
		{
			expr: `
@*_

blk {
 @x
}
`,
			src: `
a = b
blk1 {}

blk {
 blk1 {}
}
`,
			count: 1,
		},

		// expr tokenize errors
		{"$", "", tokErr(":1,2-2: wildcard must be followed by ident, got TokenEOF")},

		// expr parse errors
		{"a = ", "", parseErr(":1,3-3: Missing expression; Expected the start of an expression, but found the end of the file.")},

		// empty source
		{"", "", 1},
		{"\t", "", 1},
		{"a", "", 0},
	}

	for i, tc := range tests {
		t.Run(fmt.Sprintf("%02d", i), func(t *testing.T) {
			matchTest(t, tc.expr, tc.src, tc.count)
		})
	}
}

func TestParent(t *testing.T) {
	tests := []struct {
		expr, src string
		n         int
		expect    interface{}
	}{
		{
			expr: "x = 1",
			src: `
blk {
  x = 1
}`,
			n: 1,
			expect: `{
  x = 1
}`,
		},
		{
			expr: "x = 1",
			src: `
blk {
  x = 1
}`,
			n: 2,
			expect: `blk {
  x = 1
}`,
		},
		// Exceeding the parent boundary results into no match
		{
			expr: "x = 1",
			src: `
blk {
  x = 1
}`,
			n:      3,
			expect: 0,
		},
	}

	for i, tc := range tests {
		t.Run(fmt.Sprintf("%02d", i), func(t *testing.T) {
			parentTest(t, tc.expr, tc.src, tc.n, tc.expect)
		})
	}
}

func matchStrs(expr, src string) ([]hclsyntax.Node, error) {
	exprNode, err := compileExpr(expr)
	if err != nil {
		return nil, err
	}
	srcNode, err := compileExpr(src)
	if err != nil {
		return nil, err
	}
	m := matcher{
		out: io.Discard,
	}
	return m.matches([]cmd{
		{
			name:  "x",
			src:   expr,
			value: exprNode,
		},
	}, srcNode), nil
}

func matchTest(t *testing.T, expr, src string, anyWant interface{}) {
	tfatalf := func(format string, a ...interface{}) {
		t.Fatalf("%s | %s: %s", expr, src, fmt.Sprintf(format, a...))
	}
	matches, err := matchStrs(expr, src)
	switch want := anyWant.(type) {
	case wantErr:
		if err == nil {
			tfatalf("wanted error %q, got none", want)
		} else if got := err.Error(); got != string(want) {
			tfatalf("wanted error %q, got %q", want, got)
		}
	case int:
		if err != nil {
			tfatalf("unexpected error: %v", err)
		}
		if len(matches) != want {
			tfatalf("wanted %d matches, got=%d", want, len(matches))
		}
	default:
		panic(fmt.Sprintf("unexpected anyWant type: %T", anyWant))
	}
}

func parentTest(t *testing.T, expr, src string, n int, anyWant interface{}) {
	tfatalf := func(format string, a ...interface{}) {
		t.Fatalf("%s | %s | %d: %s", expr, src, n, fmt.Sprintf(format, a...))
	}
	matches, err := matchParentStrs(expr, src, n)
	if err != nil {
		tfatalf("unexpected error: %v", err)
	}
	switch want := anyWant.(type) {
	case wantErr:
		if err == nil {
			tfatalf("wanted error %q, got none", want)
		} else if got := err.Error(); got != string(want) {
			tfatalf("wanted error %q, got %q", want, got)
		}
	case int:
		if err != nil {
			tfatalf("unexpected error: %v", err)
		}
		if len(matches) != want {
			tfatalf("wanted %d matches, got=%d", want, len(matches))
		}
	case string:
		if err != nil {
			tfatalf("unexpected error: %v", err)
		}
		if len(matches) != 1 {
			tfatalf("unexpected multiple matches", len(matches))
		}
		m := matches[0]
		got := string(m.Range().SliceBytes([]byte(src)))
		if want != got {
			tfatalf("wanted:\n%s\ngot:\n%s\n", want, got)
		}
	default:
		panic(fmt.Sprintf("unexpected anyWant type: %T", anyWant))
	}
}

func matchParentStrs(expr, src string, n int) ([]hclsyntax.Node, error) {
	exprNode, err := compileExpr(expr)
	if err != nil {
		return nil, err
	}
	srcNode, diags := parse([]byte(src), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, errors.New(diags.Error())
	}
	m := matcher{
		out: io.Discard,
	}
	return m.matches([]cmd{
		{
			name:  "x",
			src:   expr,
			value: exprNode,
		},
		{
			name:  "p",
			src:   strconv.Itoa(n),
			value: n,
		},
	}, srcNode), nil
}
