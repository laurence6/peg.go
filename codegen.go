package peg

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const header = `
var pegErr = errors.New("PEG ERROR")

func Parse(src []rune) (interface{}, error) {
	p := parser{src, 0}
	return p.rule_%s()
}

type parser struct {
	src []rune
	n   int
}

func (__p *parser) advance(n int) {
	__p.n += n
}

func (__p *parser) backTo(n int) {
	__p.n = n
}

func (__p *parser) expectDot(advance bool) (interface{}, error) {
	if __p.n < len(__p.src) {
		r := string(__p.src[__p.n])
		if advance {
			__p.advance(1)
		}
		return r, nil
	}
	return nil, pegErr
}

func (__p *parser) expectString(advance bool, str string, l int) (interface{}, error) {
	if __p.n + l <= len(__p.src) && str == string(__p.src[__p.n:__p.n+l]) {
		if advance {
			__p.advance(l)
		}
		return str, nil
	}
	return nil, pegErr
}

func (__p *parser) expectChar(advance bool, chars ...rune) (interface{}, error) {
	if __p.n < len(__p.src) {
		c := __p.src[__p.n]
		for i := 0; i < len(chars); i += 2 {
			if chars[i] <= c && c <= chars[i+1] {
				if advance {
					__p.advance(1)
				}
				return string(c), nil
			}
		}
	}
	return nil, pegErr
}

func (__p *parser) expectCharNot(advance bool, chars ...rune) (interface{}, error) {
	if __p.n < len(__p.src) {
		c := __p.src[__p.n]
		for i := 0; i < len(chars); i += 2 {
			if chars[i] <= c && c <= chars[i+1] {
				return nil, pegErr
			}
		}
		if advance {
			__p.advance(1)
		}
		return string(c), nil
	}
	return nil, pegErr
}

func (__p *parser) zeroOrOne(pe func() (interface{}, error)) (interface{}, error) {
	if r, err := pe(); err == nil {
		return r, nil
	}
	return nil, nil
}

func (__p *parser) oneOrMore(pe func() (interface{}, error)) (interface{}, error) {
	var ret []interface{}
	if r, err := pe(); err == nil {
		ret = []interface{}{r}
	} else {
		return nil, pegErr
	}
	for {
		if r, err := pe(); err == nil {
			ret = append(ret, r)
		} else {
			break
		}
	}
	if len(ret) > 0 {
		return ret, nil
	} else {
		return nil, pegErr
	}
}

func (__p *parser) zeroOrMore(pe func() (interface{}, error)) (interface{}, error) {
	ret := []interface{}{}
	for {
		if r, err := pe(); err == nil {
			ret = append(ret, r)
		} else {
			break
		}
	}
	return ret, nil
}

func String(r interface{}) string {
	if r == nil {
		return ""
	}

	switch reflect.TypeOf(r).Kind() {
	case reflect.Array:
		fallthrough
	case reflect.Slice:
		buf := &bytes.Buffer{}
		v := reflect.ValueOf(r)
		for i := 0; i < v.Len(); i++ {
			buf.WriteString(String(v.Index(i).Interface()))
		}
		return buf.String()
	default:
		return fmt.Sprint(r)
	}

	return ""
}

func RuneSlice(r interface{}) []rune {
	return []rune(String(r))
}
`

const mainFunc = `
func main() {
	src, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	tree, err := Parse([]rune(string(src)))
	if err != nil {
		panic(err)
	}

	tree.(*Tree).GenCode(os.Stdout)
}
`

func (tree *Tree) GenCode(out io.Writer) {
	fmt.Fprintf(out, "package %s\n", tree.Package)

	for _, i := range tree.Import {
		if i.Name == "" {
			fmt.Fprintf(out, "import %q\n", i.Path)
		} else {
			fmt.Fprintf(out, "import %s %q\n", i.Name, i.Path)
		}
	}

	if tree.Package == "main" {
		fmt.Fprint(out, mainFunc)
	}

	fmt.Fprintf(out, header, tree.RuleList[0].Name)

	for _, r := range tree.RuleList {
		r.GenCode(out)
	}

	fmt.Fprint(out, tree.Grammar.Code)

	io.Copy(out, userCode)
}

func (r *Rule) GenCode(out io.Writer) {
	fmt.Fprint(out, "// Rule: ")
	r.Print(out)
	fmt.Fprintln(out, "")

	fmt.Fprintf(out, "func (__p *parser) rule_%s() (interface{}, error) {\n", r.Name)

	r.ChoiceExpr.GenCode(out)

	fmt.Fprintln(out, "return nil, pegErr")

	fmt.Fprintln(out, "}\n")
}

func (ce *ChoiceExpr) GenCode(out io.Writer) {
	fmt.Fprintln(out, "__peg_n := __p.n")
	for _, ae := range ce.ActionExprs {
		fmt.Fprintf(out, "if __ae_ret, err := ")
		ae.GenCode(out)
		fmt.Fprintf(out,
			"; err == nil {\n"+
				"	return __ae_ret, nil\n"+
				"} else {\n"+
				"	__p.backTo(__peg_n)"+
				"}\n",
		)
	}
}

func (se *SeqExpr) hasLabel() bool {
	for _, le := range se.LabeledExprs {
		if le.Label != "" {
			return true
		}
	}
	return false
}

var userCodeN uint64 = 0
var userCode = &bytes.Buffer{}

func (ae *ActionExpr) GenCode(out io.Writer) {
	fmt.Fprint(out, "func() (interface{}, error) {\n")

	vars := []string{}
	hasLabel := ae.SeqExpr.hasLabel()
	for n, le := range ae.SeqExpr.LabeledExprs {
		var varName string
		if !hasLabel {
			varName = fmt.Sprintf("__peg_v%d", n)
		} else if le.Label != "" {
			varName = le.Label
		} else {
			varName = "_"
		}

		if varName != "_" {
			vars = append(vars, varName)
			fmt.Fprintf(out, "var %s interface{}\n", varName)
		}

		var retVarName string
		not := "="
		if le.PrefixedExpr.PrefixOp == AND || le.PrefixedExpr.PrefixOp == NOT {
			retVarName = "_"
			if le.PrefixedExpr.PrefixOp == NOT {
				not = "!"
			}
		} else if varName == "_" {
			retVarName = "_"
		} else {
			retVarName = "__pe_ret"
		}

		fmt.Fprintf(out, "if %s, err := ", retVarName)

		le.PrefixedExpr.GenCode(out)

		fmt.Fprintf(out, "; err %s= nil {\n", not)
		if retVarName != "_" {
			fmt.Fprintf(out, "	%s = %s\n", varName, retVarName)
		}
		fmt.Fprint(out,
			"} else {\n"+
				"	return nil, pegErr\n"+
				"}\n",
		)
	}

	if ae.Code != "" {
		var paramsDef string
		var paramsCall string
		if hasLabel {
			paramsDef = fmt.Sprintf("%s interface{}", strings.Join(vars, ", "))
			paramsCall = strings.Join(vars, ", ")
		} else {
			if len(vars) == 1 {
				paramsDef = fmt.Sprintf("result interface{}")
				paramsCall = fmt.Sprintf("%s", vars[0])
			} else {
				paramsDef = fmt.Sprintf("result [%d]interface{}", len(vars))
				paramsCall = fmt.Sprintf("[...]interface{}{%s}", strings.Join(vars, ", "))
			}
		}

		userCode.WriteString(
			fmt.Sprintf(
				"func (__p *parser) ae_code_%d(%s) (ret interface{}) {\n"+
					"	%s\n"+
					"	return\n"+
					"}\n",
				userCodeN,
				paramsDef,
				ae.Code,
			),
		)

		fmt.Fprintf(out, "return __p.ae_code_%d(%s), nil\n",
			userCodeN,
			paramsCall,
		)

		userCodeN++
	} else {
		if len(vars) > 1 {
			fmt.Fprintf(out, "return [...]interface{}{%s}, nil\n", strings.Join(vars, ", "))
		} else {
			fmt.Fprintf(out, "return %s, nil\n", vars[0])
		}
	}

	fmt.Fprint(out, "}()")
}

var advance = true

func (pe *PrefixedExpr) GenCode(out io.Writer) {
	fmt.Fprintln(out, "func() (interface{}, error) {")

	if advance && (pe.PrefixOp == AND || pe.PrefixOp == NOT) {
		advance = false
		defer func() {
			advance = true
		}()
	}

	if pe.SuffixedExpr.SuffixOp != 0 {
		fmt.Fprint(out, "// PrefixedExpr: ")
		pe.Print(out)
		fmt.Fprintln(out, "")

		fmt.Fprintln(out, "__peg_pe := func() (interface{}, error) {")
		pe.SuffixedExpr.PrimaryExpr.GenCode(out)
		fmt.Fprintln(out,
			"	return nil, pegErr\n"+
				"}",
		)

		switch pe.SuffixedExpr.SuffixOp {
		case QUESTION: // 0-1
			fmt.Fprintf(out, "return __p.zeroOrOne(__peg_pe)\n")
		case PLUS: // 1-
			fmt.Fprintf(out, "return __p.oneOrMore(__peg_pe)\n")
		case STAR: // 0-
			fmt.Fprintf(out, "return __p.zeroOrMore(__peg_pe)\n")
		}
	} else {
		pe.SuffixedExpr.PrimaryExpr.GenCode(out)
	}

	fmt.Fprint(out, "return nil, pegErr\n}()")
}

func (pe *PrimaryExpr) GenCode(out io.Writer) {
	fmt.Fprint(out, "// PrimaryExpr: ")
	pe.Print(out)
	fmt.Fprintln(out, "")

	switch pe.PrimaryExpr.(type) {
	case *Matcher:
		pe.PrimaryExpr.(*Matcher).GenCode(out)
	case string:
		fmt.Fprintf(out,
			"return __p.rule_%s()\n",
			pe.PrimaryExpr.(string))
	case *ChoiceExpr:
		pe.PrimaryExpr.(*ChoiceExpr).GenCode(out)
	default:
		panic("type of PrimaryExpr should be *Matcher, string, *ChoiceExpr")
	}
}

func serializeCharRange(chars []*Char) string {
	buf := &bytes.Buffer{}
	for _, c := range chars {
		buf.WriteString(strconv.QuoteRune(c.Start))
		buf.WriteString(", ")
		buf.WriteString(strconv.QuoteRune(c.End))
		buf.WriteString(", ")
	}
	return buf.String()
}

func (m *Matcher) GenCode(out io.Writer) {
	switch m.Matcher.(type) {
	case int:
		fmt.Fprintf(out,
			"return __p.expectDot(%t)\n",
			advance)
	case string:
		str := m.Matcher.(string)
		l := utf8.RuneCountInString(str)
		fmt.Fprintf(out,
			"return __p.expectString(%t, %q, %d)\n",
			advance, str, l)
	case *CharRange:
		funcName := "expectChar"
		if m.Matcher.(*CharRange).Not {
			funcName = "expectCharNot"
		}
		fmt.Fprintf(out,
			"return __p.%s(%t, %s)\n",
			funcName,
			advance, serializeCharRange(m.Matcher.(*CharRange).Chars),
		)
	default:
		panic("type of Matcher should be int, string, *CharRange")
	}
}
