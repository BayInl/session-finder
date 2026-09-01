package index

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query parsing deliberately happens outside SQLite. FTS5 MATCH accepts its own
// operators, so passing a user's complete expression to it would make the
// command's grammar depend on SQLite and would also make punctuation unsafe.
type queryTokenKind uint8

const (
	queryWord queryTokenKind = iota
	queryPhrase
	queryLParen
	queryRParen
	queryOperator
)

type queryToken struct {
	kind  queryTokenKind
	text  string
	raw   string
	pos   int
	quote bool
}

type queryAtom struct {
	value  string
	phrase bool
	key    string
}

type queryExprKind uint8

const (
	queryAtomExpr queryExprKind = iota
	queryNotExpr
	queryAndExpr
	queryOrExpr
)

type queryExpr struct {
	kind        queryExprKind
	atom        queryAtom
	left, right *queryExpr
	legacyAND   bool
}

type queryFilters struct {
	tools  []string
	cwds   []string
	afters []string
}

func lexQuery(input string) ([]queryToken, error) {
	tokens := make([]queryToken, 0, 8)
	for i := 0; i < len(input); {
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
		}
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		if r == '(' {
			tokens = append(tokens, queryToken{kind: queryLParen, raw: input[i : i+size], pos: i})
			i += size
			continue
		}
		if r == ')' {
			tokens = append(tokens, queryToken{kind: queryRParen, raw: input[i : i+size], pos: i})
			i += size
			continue
		}

		start := i
		var value strings.Builder
		hadQuote := false
		hadEscape := false
		for i < len(input) {
			r, size = utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size == 1 {
				return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
			}
			if unicode.IsSpace(r) || r == '(' || r == ')' {
				break
			}
			if r == '\\' {
				hadEscape = true
				i += size
				if i >= len(input) {
					return nil, fmt.Errorf("dangling escape at byte %d", i-1)
				}
				escaped, escapedSize := utf8.DecodeRuneInString(input[i:])
				if escaped == utf8.RuneError && escapedSize == 1 {
					return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
				}
				value.WriteRune(escaped)
				i += escapedSize
				continue
			}
			if r == '"' {
				hadQuote = true
				i += size
				closed := false
				for i < len(input) {
					quoted, quotedSize := utf8.DecodeRuneInString(input[i:])
					if quoted == utf8.RuneError && quotedSize == 1 {
						return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
					}
					if quoted == '\\' {
						i += quotedSize
						if i >= len(input) {
							return nil, fmt.Errorf("dangling escape at byte %d", i-1)
						}
						escaped, escapedSize := utf8.DecodeRuneInString(input[i:])
						if escaped == utf8.RuneError && escapedSize == 1 {
							return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
						}
						value.WriteRune(escaped)
						i += escapedSize
						continue
					}
					if quoted == '"' {
						i += quotedSize
						closed = true
						break
					}
					value.WriteRune(quoted)
					i += quotedSize
				}
				if !closed {
					return nil, fmt.Errorf("unterminated quote at byte %d", start)
				}
				continue
			}
			value.WriteRune(r)
			i += size
		}
		raw := input[start:i]
		text := value.String()
		kind := queryWord
		if hadQuote && strings.HasPrefix(raw, "\"") {
			kind = queryPhrase
		}
		if kind == queryWord && !hadEscape {
			upper := strings.ToUpper(text)
			switch upper {
			case "AND", "OR", "NOT":
				kind = queryOperator
			}
		}
		tokens = append(tokens, queryToken{kind: kind, text: text, raw: raw, pos: start, quote: kind == queryPhrase})
	}
	return tokens, nil
}

func splitFieldToken(token queryToken) (string, string, bool, error) {
	if token.kind != queryWord || token.raw == "" {
		return "", "", false, nil
	}
	colon := -1
	for i := 0; i < len(token.raw); i++ {
		if token.raw[i] != ':' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && token.raw[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			colon = i
			break
		}
	}
	if colon <= 0 || colon == len(token.raw)-1 {
		return "", "", false, nil
	}
	name := strings.ToLower(token.raw[:colon])
	if name != "tool" && name != "cwd" && name != "after" {
		return "", "", false, nil
	}
	value, err := decodeQueryFragment(token.raw[colon+1:])
	if err != nil {
		return "", "", false, fmt.Errorf("invalid %s filter: %w", name, err)
	}
	if value == "" {
		return "", "", false, nil
	}
	return name, value, true, nil
}

func decodeQueryFragment(raw string) (string, error) {
	var value strings.Builder
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size == 1 {
			return "", fmt.Errorf("invalid UTF-8")
		}
		if r == '\\' {
			i += size
			if i >= len(raw) {
				return "", errors.New("dangling escape")
			}
			escaped, escapedSize := utf8.DecodeRuneInString(raw[i:])
			if escaped == utf8.RuneError && escapedSize == 1 {
				return "", errors.New("invalid UTF-8")
			}
			value.WriteRune(escaped)
			i += escapedSize
			continue
		}
		if r == '"' {
			i += size
			closed := false
			for i < len(raw) {
				quoted, quotedSize := utf8.DecodeRuneInString(raw[i:])
				if quoted == '\\' {
					i += quotedSize
					if i >= len(raw) {
						return "", errors.New("dangling escape")
					}
					escaped, escapedSize := utf8.DecodeRuneInString(raw[i:])
					value.WriteRune(escaped)
					i += escapedSize
					continue
				}
				if quoted == '"' {
					i += quotedSize
					closed = true
					break
				}
				value.WriteRune(quoted)
				i += quotedSize
			}
			if !closed {
				return "", errors.New("unterminated quote")
			}
			continue
		}
		value.WriteRune(r)
		i += size
	}
	return value.String(), nil
}

type queryParser struct {
	tokens []queryToken
	pos    int
}

func parseQuery(input string) (*queryExpr, queryFilters, error) {
	tokens, err := lexQuery(input)
	if err != nil {
		return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
	}
	filters := queryFilters{}
	terms := make([]queryToken, 0, len(tokens))
	for _, token := range tokens {
		name, value, ok, err := splitFieldToken(token)
		if err != nil {
			return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
		}
		if !ok {
			terms = append(terms, token)
			continue
		}
		switch name {
		case "tool":
			filters.tools = append(filters.tools, value)
		case "cwd":
			filters.cwds = append(filters.cwds, value)
		case "after":
			filters.afters = append(filters.afters, value)
		}
	}
	if len(terms) == 0 {
		return nil, filters, nil
	}
	parser := queryParser{tokens: terms}
	expr, err := parser.parseOr()
	if err != nil {
		return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
	}
	if parser.pos != len(parser.tokens) {
		token := parser.tokens[parser.pos]
		return nil, queryFilters{}, fmt.Errorf("invalid query: unexpected %q at byte %d", token.text, token.pos)
	}
	if !hasPositiveQueryTerm(expr, false) {
		return nil, queryFilters{}, errors.New("invalid query: NOT requires a positive term")
	}
	return expr, filters, nil
}

func (p *queryParser) parseOr() (*queryExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "OR") {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, errors.New("expected expression after OR")
		}
		left = &queryExpr{kind: queryOrExpr, left: left, right: right}
	}
	return left, nil
}

func (p *queryParser) parseAnd() (*queryExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "AND") {
			p.pos++
			right, err := p.parseUnary()
			if err != nil {
				return nil, errors.New("expected expression after AND")
			}
			left = &queryExpr{kind: queryAndExpr, left: left, right: right}
			continue
		}
		if p.startsExpression() {
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &queryExpr{kind: queryAndExpr, left: left, right: right, legacyAND: true}
			continue
		}
		break
	}
	return left, nil
}

func (p *queryParser) parseUnary() (*queryExpr, error) {
	if p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "NOT") {
		p.pos++
		if !p.startsExpression() {
			return nil, errors.New("expected expression after NOT")
		}
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &queryExpr{kind: queryNotExpr, left: child}, nil
	}
	return p.parsePrimary()
}

func (p *queryParser) parsePrimary() (*queryExpr, error) {
	if p.pos >= len(p.tokens) {
		return nil, errors.New("expected expression")
	}
	token := p.tokens[p.pos]
	switch token.kind {
	case queryWord, queryPhrase:
		p.pos++
		if token.text == "" {
			return nil, errors.New("empty term")
		}
		return &queryExpr{kind: queryAtomExpr, atom: queryAtom{value: token.text, phrase: token.kind == queryPhrase,
			key: queryAtomKey(token.text, token.kind == queryPhrase)}}, nil
	case queryLParen:
		p.pos++
		if p.peekKind(queryRParen) {
			return nil, errors.New("empty parentheses")
		}
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.peekKind(queryRParen) {
			return nil, errors.New("missing closing parenthesis")
		}
		p.pos++
		return expr, nil
	case queryOperator:
		return nil, fmt.Errorf("unexpected operator %q", token.text)
	case queryRParen:
		return nil, errors.New("unexpected closing parenthesis")
	default:
		return nil, errors.New("expected expression")
	}
}

func (p *queryParser) peekKind(kind queryTokenKind) bool {
	return p.pos < len(p.tokens) && p.tokens[p.pos].kind == kind
}

func (p *queryParser) startsExpression() bool {
	if p.pos >= len(p.tokens) {
		return false
	}
	token := p.tokens[p.pos]
	if token.kind == queryWord || token.kind == queryPhrase || token.kind == queryLParen {
		return true
	}
	return token.kind == queryOperator && strings.EqualFold(token.text, "NOT")
}

func queryAtomKey(value string, phrase bool) string {
	if phrase {
		return "phrase\x00" + value
	}
	return "term\x00" + value
}

func hasPositiveQueryTerm(expr *queryExpr, negated bool) bool {
	if expr == nil {
		return false
	}
	switch expr.kind {
	case queryAtomExpr:
		return !negated
	case queryNotExpr:
		return hasPositiveQueryTerm(expr.left, !negated)
	default:
		return hasPositiveQueryTerm(expr.left, negated) || hasPositiveQueryTerm(expr.right, negated)
	}
}

func collectQueryAtoms(expr *queryExpr, negated bool, atoms *[]queryAtom) {
	if expr == nil {
		return
	}
	switch expr.kind {
	case queryAtomExpr:
		if !negated {
			*atoms = append(*atoms, expr.atom)
		}
	case queryNotExpr:
		collectQueryAtoms(expr.left, !negated, atoms)
	default:
		collectQueryAtoms(expr.left, negated, atoms)
		collectQueryAtoms(expr.right, negated, atoms)
	}
}

// PositiveTerms returns unique positive query atoms for highlighting.
// Invalid or filter-only queries return nil.
func PositiveTerms(query string) []string {
	expr, _, err := parseQuery(query)
	if err != nil || expr == nil {
		return nil
	}
	var atoms []queryAtom
	collectQueryAtoms(expr, false, &atoms)
	seen := make(map[string]bool, len(atoms))
	terms := make([]string, 0, len(atoms))
	for _, atom := range atoms {
		if seen[atom.key] {
			continue
		}
		seen[atom.key] = true
		terms = append(terms, atom.value)
	}
	sort.SliceStable(terms, func(i, j int) bool {
		return len([]rune(terms[i])) > len([]rune(terms[j]))
	})
	return terms
}

func cloneIDSet(values map[int64]struct{}) map[int64]struct{} {
	result := make(map[int64]struct{}, len(values))
	for id := range values {
		result[id] = struct{}{}
	}
	return result
}

func sessionKey(row searchMessage) string {
	return row.tool + "\x00" + row.sessionID
}

func sessionSet(ids map[int64]struct{}, universe map[int64]searchMessage) map[string]struct{} {
	result := make(map[string]struct{})
	for id := range ids {
		if row, ok := universe[id]; ok {
			result[sessionKey(row)] = struct{}{}
		}
	}
	return result
}

// implicitSessionAND preserves the original whitespace-query contract: terms
// separated only by spaces are required to occur in the same session, not the
// same message. The returned IDs are the matching message IDs from that
// session, just like the legacy per-term FTS aggregation.
func implicitSessionAND(left, right map[int64]struct{}, universe map[int64]searchMessage) map[int64]struct{} {
	leftSessions := sessionSet(left, universe)
	rightSessions := sessionSet(right, universe)
	common := make(map[string]struct{})
	for key := range leftSessions {
		if _, ok := rightSessions[key]; ok {
			common[key] = struct{}{}
		}
	}
	result := make(map[int64]struct{})
	for id := range left {
		if row, ok := universe[id]; ok {
			if _, ok := common[sessionKey(row)]; ok {
				result[id] = struct{}{}
			}
		}
	}
	for id := range right {
		if row, ok := universe[id]; ok {
			if _, ok := common[sessionKey(row)]; ok {
				result[id] = struct{}{}
			}
		}
	}
	return result
}

func intersectIDSets(left, right map[int64]struct{}) map[int64]struct{} {
	if len(left) > len(right) {
		left, right = right, left
	}
	result := make(map[int64]struct{}, len(left))
	for id := range left {
		if _, ok := right[id]; ok {
			result[id] = struct{}{}
		}
	}
	return result
}

func exprContainsNot(expr *queryExpr) bool {
	if expr == nil {
		return false
	}
	if expr.kind == queryNotExpr {
		return true
	}
	return exprContainsNot(expr.left) || exprContainsNot(expr.right)
}

func evalQuery(expr *queryExpr, universeIDs map[int64]struct{}, universe map[int64]searchMessage, atoms map[string]map[int64]struct{}) map[int64]struct{} {
	if expr == nil {
		return cloneIDSet(universeIDs)
	}
	switch expr.kind {
	case queryAtomExpr:
		return cloneIDSet(atoms[expr.atom.key])
	case queryNotExpr:
		child := evalQuery(expr.left, universeIDs, universe, atoms)
		result := make(map[int64]struct{}, len(universeIDs))
		for id := range universeIDs {
			if _, excluded := child[id]; !excluded {
				result[id] = struct{}{}
			}
		}
		return result
	case queryAndExpr:
		left := evalQuery(expr.left, universeIDs, universe, atoms)
		right := evalQuery(expr.right, universeIDs, universe, atoms)
		if expr.legacyAND && !exprContainsNot(expr.left) && !exprContainsNot(expr.right) {
			return implicitSessionAND(left, right, universe)
		}
		return intersectIDSets(left, right)
	case queryOrExpr:
		left := evalQuery(expr.left, universeIDs, universe, atoms)
		right := evalQuery(expr.right, universeIDs, universe, atoms)
		for id := range right {
			left[id] = struct{}{}
		}
		return left
	default:
		return map[int64]struct{}{}
	}
}

type searchMessage struct {
	id                          int64
	tool, sessionID, title, cwd string
	sourcePath, role            string
	created, updated, ts        any
}

func parseAfterFilter(value string) (float64, error) {
	epoch, ok := TimestampEpoch(value)
	if !ok || epoch == nil {
		return 0, errors.New("after must be YYYY-MM-DD")
	}
	return *epoch, nil
}

func searchFilterClauses(tool, cwd, after string, filters queryFilters, includeSystem bool) ([]string, []any, error) {
	where := make([]string, 0, 4+len(filters.tools)+len(filters.cwds)+len(filters.afters))
	params := make([]any, 0, cap(where))
	if !includeSystem {
		where = append(where, "m.role IN ('user', 'assistant')")
	}
	if tool != "" {
		where = append(where, "s.tool = ?")
		params = append(params, tool)
	}
	if cwd != "" {
		where = append(where, `s.cwd LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escapeLike(cwd)+"%")
	}
	if after != "" {
		epoch, err := parseAfterFilter(after)
		if err != nil {
			return nil, nil, err
		}
		where = append(where, "m.ts IS NOT NULL AND m.ts >= ?")
		params = append(params, epoch)
	}
	for _, value := range filters.tools {
		where = append(where, "s.tool = ?")
		params = append(params, value)
	}
	for _, value := range filters.cwds {
		where = append(where, `s.cwd LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escapeLike(value)+"%")
	}
	for _, value := range filters.afters {
		epoch, err := parseAfterFilter(value)
		if err != nil {
			return nil, nil, err
		}
		where = append(where, "m.ts IS NOT NULL AND m.ts >= ?")
		params = append(params, epoch)
	}
	return where, params, nil
}

func searchUniverse(db *sql.DB, tool, cwd, after string, filters queryFilters, includeSystem bool) (map[int64]searchMessage, error) {
	where, params, err := searchFilterClauses(tool, cwd, after, filters, includeSystem)
	if err != nil {
		return nil, err
	}
	query := `SELECT m.id, s.tool, s.session_id, s.title, s.cwd, s.created, s.updated,
		s.source_path, m.ts, m.role FROM messages AS m JOIN sessions AS s ON s.id = m.session_pk`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY m.id"
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]searchMessage)
	for rows.Next() {
		var row searchMessage
		if err := rows.Scan(&row.id, &row.tool, &row.sessionID, &row.title, &row.cwd, &row.created,
			&row.updated, &row.sourcePath, &row.ts, &row.role); err != nil {
			return nil, err
		}
		result[row.id] = row
	}
	return result, rows.Err()
}

func searchUniverseByIDs(db *sql.DB, ids []int64, tool, cwd, after string, filters queryFilters, includeSystem bool) (map[int64]searchMessage, error) {
	result := make(map[int64]searchMessage, len(ids))
	// SQLite's default variable limit is 32766; leave room for filters while
	// keeping common result sets in a single index-driven query.
	const chunkSize = 30000
	for start := 0; start < len(ids); start += chunkSize {
		end := minInt(len(ids), start+chunkSize)
		where, params, err := searchFilterClauses(tool, cwd, after, filters, includeSystem)
		if err != nil {
			return nil, err
		}
		placeholders := make([]string, end-start)
		idParams := make([]any, 0, end-start+len(params))
		for i, id := range ids[start:end] {
			placeholders[i] = "?"
			idParams = append(idParams, id)
		}
		where = append([]string{"m.id IN (" + strings.Join(placeholders, ",") + ")"}, where...)
		idParams = append(idParams, params...)
		query := `SELECT m.id, s.tool, s.session_id, s.title, s.cwd, s.created, s.updated,
			s.source_path, m.ts, m.role FROM messages AS m JOIN sessions AS s ON s.id = m.session_pk WHERE ` + strings.Join(where, " AND ") + " ORDER BY m.id"
		rows, err := db.Query(query, idParams...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row searchMessage
			if err := rows.Scan(&row.id, &row.tool, &row.sessionID, &row.title, &row.cwd, &row.created,
				&row.updated, &row.sourcePath, &row.ts, &row.role); err != nil {
				rows.Close()
				return nil, err
			}
			result[row.id] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func containsSearchPunctuation(value string) bool {
	for _, r := range value {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return true
	}
	return false
}

// legacySearchTokens approximates unicode61's token boundaries for terms that
// contain punctuation. In particular, session-finder matches adjacent tokens
// in both "session-finder" and "/path/session-finder/", as the old FTS query
// did, while plain CJK and word terms retain literal substring behavior.
func legacySearchTokens(value string) []string {
	lowered := strings.ToLower(value)
	var tokens []string
	start := -1
	for i, r := range lowered {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tokens = append(tokens, lowered[start:i])
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, lowered[start:])
	}
	return tokens
}

func sortedUniverseIDs(universe map[int64]searchMessage) []int64 {
	ids := make([]int64, 0, len(universe))
	for id := range universe {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func queryLikeCandidates(db *sql.DB, value, tool, cwd, after string, filters queryFilters, includeSystem bool) (map[int64]struct{}, error) {
	where, filterParams, err := searchFilterClauses(tool, cwd, after, filters, includeSystem)
	if err != nil {
		return nil, err
	}
	where = append([]string{"m.text LIKE ? ESCAPE '\\'"}, where...)
	params := make([]any, 0, len(filterParams)+1)
	params = append(params, "%"+escapeLike(value)+"%")
	params = append(params, filterParams...)
	query := `SELECT m.id FROM messages AS m JOIN sessions AS s ON s.id = m.session_pk WHERE ` + strings.Join(where, " AND ") + " ORDER BY m.id LIMIT 50000"
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func candidateIDsForTerm(db *sql.DB, value, tool, cwd, after string, filters queryFilters, includeSystem bool) (map[int64]struct{}, error) {
	set := make(map[int64]struct{})
	where, filterParams, err := searchFilterClauses(tool, cwd, after, filters, includeSystem)
	if err != nil {
		return nil, err
	}
	queries := []struct {
		table string
		match string
	}{
		{table: "messages_fts", match: `"` + strings.ReplaceAll(value, `"`, `""`) + `"`},
	}
	if utf8.RuneCountInString(value) >= 3 {
		queries = append(queries, struct {
			table string
			match string
		}{table: "messages_tri", match: `"` + strings.ReplaceAll(value, `"`, `""`) + `"`})
	}
	for _, candidate := range queries {
		queryWhere := append([]string{"m.id IN (SELECT rowid FROM " + candidate.table + " WHERE " + candidate.table + " MATCH ?)"}, where...)
		params := make([]any, 0, len(filterParams)+1)
		params = append(params, candidate.match)
		params = append(params, filterParams...)
		query := `SELECT m.id FROM messages AS m JOIN sessions AS s ON s.id = m.session_pk WHERE ` + strings.Join(queryWhere, " AND ") + " ORDER BY m.id LIMIT 50000"
		rows, err := db.Query(query, params...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			set[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	// unicode61 can miss unspaced CJK and short/edge terms even when FTS
	// returns other candidates. Union the filtered LIKE results for those
	// terms while keeping the broad-query path index-driven.
	if utf8.RuneCountInString(value) < 3 || IsCJK(value) {
		likeSet, err := queryLikeCandidates(db, value, tool, cwd, after, filters, includeSystem)
		if err != nil {
			return nil, err
		}
		for id := range likeSet {
			set[id] = struct{}{}
		}
	}
	return set, nil
}

func mapValues(values map[string]map[int64]struct{}) []map[int64]struct{} {
	sets := make([]map[int64]struct{}, 0, len(values))
	for _, set := range values {
		sets = append(sets, set)
	}
	return sets
}

func unionCandidateIDs(sets []map[int64]struct{}) []int64 {
	idsSet := make(map[int64]struct{})
	for _, set := range sets {
		for id := range set {
			idsSet[id] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func atomCandidateSets(db *sql.DB, tool, cwd, after string, filters queryFilters, includeSystem bool, atoms []queryAtom) (map[string]map[int64]struct{}, error) {
	sets := make(map[string]map[int64]struct{}, len(atoms))
	for _, atom := range atoms {
		if _, exists := sets[atom.key]; exists {
			continue
		}
		var set map[int64]struct{}
		if !atom.phrase && containsSearchPunctuation(atom.value) {
			// The legacy query path tokenized punctuation-delimited values and
			// required every token in the same session. Keep all token matches,
			// including matches that live in different messages, while avoiding
			// unrelated messages from that session.
			tokens := legacySearchTokens(atom.value)
			seenTokens := make(map[string]bool, len(tokens))
			tokenSets := make([]map[int64]struct{}, 0, len(tokens))
			for _, token := range tokens {
				if seenTokens[token] {
					continue
				}
				seenTokens[token] = true
				candidateSet, err := candidateIDsForTerm(db, token, tool, cwd, after, filters, includeSystem)
				if err != nil {
					return nil, err
				}
				tokenSets = append(tokenSets, candidateSet)
			}
			set = make(map[int64]struct{})
			if len(tokenSets) == 0 {
				var err error
				set, err = queryLikeCandidates(db, atom.value, tool, cwd, after, filters, includeSystem)
				if err != nil {
					return nil, err
				}
			} else {
				ids := unionCandidateIDs(tokenSets)
				metadata, err := searchUniverseByIDs(db, ids, tool, cwd, after, filters, includeSystem)
				if err != nil {
					return nil, err
				}
				commonSessions := sessionSet(tokenSets[0], metadata)
				for _, tokenSet := range tokenSets[1:] {
					sessions := sessionSet(tokenSet, metadata)
					for key := range commonSessions {
						if _, ok := sessions[key]; !ok {
							delete(commonSessions, key)
						}
					}
				}
				for _, tokenSet := range tokenSets {
					for id := range tokenSet {
						if row, ok := metadata[id]; ok {
							if _, ok := commonSessions[sessionKey(row)]; ok {
								set[id] = struct{}{}
							}
						}
					}
				}
			}
		} else {
			var err error
			set, err = candidateIDsForTerm(db, atom.value, tool, cwd, after, filters, includeSystem)
			if err != nil {
				return nil, err
			}
		}
		sets[atom.key] = set
	}
	return sets, nil
}

func loadMessageTexts(db *sql.DB, ids []int64) (map[int64]string, error) {
	result := make(map[int64]string, len(ids))
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := minInt(len(ids), start+chunkSize)
		placeholders := make([]string, end-start)
		args := make([]any, end-start)
		for i, id := range ids[start:end] {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := db.Query("SELECT id, text FROM messages WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var text string
			if err := rows.Scan(&id, &text); err != nil {
				rows.Close()
				return nil, err
			}
			result[id] = text
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func manualSnippetForTerms(text string, terms []string, maxChars int) string {
	compact := strings.Join(strings.Fields(text), " ")
	if len([]rune(compact)) <= maxChars {
		return compact
	}
	lowered := strings.ToLower(compact)
	position := -1
	for _, term := range terms {
		if term == "" {
			continue
		}
		found := strings.Index(lowered, strings.ToLower(term))
		if found >= 0 && (position < 0 || found < position) {
			position = found
		}
	}
	if position < 0 {
		runes := []rune(compact)
		if maxChars <= 1 {
			return string(runes[:minInt(maxChars, len(runes))])
		}
		return string(runes[:maxChars-1]) + "…"
	}
	runePosition := len([]rune(compact[:position]))
	runes := []rune(compact)
	start := maxInt(0, runePosition-maxChars/3)
	end := minInt(len(runes), start+maxChars)
	if end-start < maxChars {
		start = maxInt(0, end-maxChars)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + string([]rune(snippet)[1:])
	}
	if end < len(runes) {
		s := []rune(snippet)
		snippet = string(s[:len(s)-1]) + "…"
	}
	return string([]rune(snippet)[:minInt(maxChars, len([]rune(snippet)))])
}

// Search parses the query, evaluates boolean expressions over message IDs, and
// only then aggregates matching messages into sessions.
func Search(db *sql.DB, query, tool, cwd, after string, limit int, includeSystem bool) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchResult{}, nil
	}
	expr, filters, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	var positiveAtoms []queryAtom
	collectQueryAtoms(expr, false, &positiveAtoms)
	atoms := make([]queryAtom, 0, len(positiveAtoms))
	seenAtoms := make(map[string]bool)
	for _, atom := range positiveAtoms {
		if !seenAtoms[atom.key] {
			seenAtoms[atom.key] = true
			atoms = append(atoms, atom)
		}
	}
	// A negative child still needs a candidate set, even though it does not
	// contribute to snippets or positive-term ranking.
	var allExprAtoms func(*queryExpr)
	allExprAtoms = func(node *queryExpr) {
		if node == nil {
			return
		}
		if node.kind == queryAtomExpr {
			if !seenAtoms[node.atom.key] {
				seenAtoms[node.atom.key] = true
				atoms = append(atoms, node.atom)
			}
			return
		}
		allExprAtoms(node.left)
		allExprAtoms(node.right)
	}
	allExprAtoms(expr)
	candidateSets, err := atomCandidateSets(db, tool, cwd, after, filters, includeSystem, atoms)
	if err != nil {
		return nil, err
	}
	candidateIDs := unionCandidateIDs(mapValues(candidateSets))
	var universe map[int64]searchMessage
	if expr == nil || exprContainsNot(expr) {
		universe, err = searchUniverse(db, tool, cwd, after, filters, includeSystem)
	} else {
		universe, err = searchUniverseByIDs(db, candidateIDs, tool, cwd, after, filters, includeSystem)
	}
	if err != nil {
		return nil, err
	}
	allIDs := make(map[int64]struct{}, len(universe))
	for id := range universe {
		allIDs[id] = struct{}{}
	}
	texts := make(map[int64]string)
	matchedIDs := evalQuery(expr, allIDs, universe, candidateSets)
	if len(matchedIDs) == 0 {
		return []SearchResult{}, nil
	}
	orderedIDs := make([]int64, 0, len(matchedIDs))
	for id := range matchedIDs {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(i, j int) bool { return orderedIDs[i] < orderedIDs[j] })

	type searchGroup struct {
		result SearchResult
		ids    []int64
	}
	groups := make(map[string]*searchGroup)
	order := make([]string, 0)
	for _, id := range orderedIDs {
		row := universe[id]
		key := row.tool + "\x00" + row.sessionID
		group := groups[key]
		if group == nil {
			group = &searchGroup{result: SearchResult{Tool: row.tool, SessionID: row.sessionID,
				Title: row.title, CWD: row.cwd, Created: FormatTimestamp(row.created), Updated: FormatTimestamp(row.updated),
				Snippets: []string{}, SourcePaths: []string{}, matchedTerms: map[string]bool{},
				createdEpoch: sqlFloat(row.created), updatedEpoch: sqlFloat(row.updated)}}
			groups[key] = group
			order = append(order, key)
		}
		group.result.MessageCount++
		if len(group.ids) < 6 {
			group.ids = append(group.ids, id)
		}
		group.result.createdEpoch = mergeMin(group.result.createdEpoch, sqlFloat(row.created))
		group.result.updatedEpoch = mergeMax(group.result.updatedEpoch, sqlFloat(row.updated))
		if group.result.Title == "" && row.title != "" {
			group.result.Title = row.title
		}
		if group.result.CWD == "" && row.cwd != "" {
			group.result.CWD = row.cwd
		}
		group.result.Created = FormatTimestamp(timestampValue(group.result.createdEpoch))
		group.result.Updated = FormatTimestamp(timestampValue(group.result.updatedEpoch))
		if !containsString(group.result.SourcePaths, row.sourcePath) {
			group.result.SourcePaths = append(group.result.SourcePaths, row.sourcePath)
		}
		for _, atom := range positiveAtoms {
			if set := candidateSets[atom.key]; set != nil {
				if _, ok := set[id]; ok {
					group.result.matchedTerms[atom.key] = true
				}
			}
		}
	}
	results := make([]SearchResult, 0, len(order))
	for _, key := range order {
		results = append(results, groups[key].result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		left, right := results[i], results[j]
		if len(left.matchedTerms) != len(right.matchedTerms) {
			return len(left.matchedTerms) > len(right.matchedTerms)
		}
		if left.MessageCount != right.MessageCount {
			return left.MessageCount > right.MessageCount
		}
		leftUpdated, rightUpdated := left.updatedEpoch, right.updatedEpoch
		if leftUpdated == nil || rightUpdated == nil {
			if leftUpdated != rightUpdated {
				return leftUpdated != nil
			}
		} else if *leftUpdated != *rightUpdated {
			return *leftUpdated > *rightUpdated
		}
		if left.Tool != right.Tool {
			return left.Tool < right.Tool
		}
		return left.SessionID < right.SessionID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	termsForSnippet := make([]string, 0, len(positiveAtoms))
	seenSnippetTerms := make(map[string]bool)
	for _, atom := range positiveAtoms {
		if !seenSnippetTerms[atom.value] {
			seenSnippetTerms[atom.value] = true
			termsForSnippet = append(termsForSnippet, atom.value)
		}
	}
	snippetIDs := make([]int64, 0)
	for _, result := range results {
		group := groups[result.Tool+"\x00"+result.SessionID]
		sort.Slice(group.ids, func(a, b int) bool { return group.ids[a] < group.ids[b] })
		for _, id := range group.ids {
			if _, ok := texts[id]; !ok {
				snippetIDs = append(snippetIDs, id)
			}
		}
	}
	if len(snippetIDs) > 0 {
		moreTexts, err := loadMessageTexts(db, snippetIDs)
		if err != nil {
			return nil, err
		}
		for id, text := range moreTexts {
			texts[id] = text
		}
	}
	for i := range results {
		group := groups[results[i].Tool+"\x00"+results[i].SessionID]
		for _, id := range group.ids {
			snippet := manualSnippetForTerms(texts[id], termsForSnippet, 200)
			if snippet != "" && !containsString(results[i].Snippets, snippet) {
				results[i].Snippets = append(results[i].Snippets, snippet)
			}
			if len(results[i].Snippets) >= 3 {
				break
			}
		}
		results[i].matchedTerms = nil
		results[i].updatedEpoch = nil
	}
	if err := AttachLastRounds(db, results); err != nil {
		return nil, err
	}
	return results, nil
}

// Kept for tests and callers that want to inspect the parser without opening
// SQLite. It intentionally returns a nil expression for a field-only query.
func parseSearchQuery(query string) (*queryExpr, queryFilters, error) {
	return parseQuery(query)
}

// queryDebugString is intentionally small and stable for parser tests and
// diagnostics; callers should use Search for actual result retrieval.
func queryDebugString(query string) (string, error) {
	expr, filters, err := parseQuery(query)
	if err != nil {
		return "", err
	}
	var render func(*queryExpr) string
	render = func(node *queryExpr) string {
		if node == nil {
			return ""
		}
		switch node.kind {
		case queryAtomExpr:
			if node.atom.phrase {
				return `"` + node.atom.value + `"`
			}
			return node.atom.value
		case queryNotExpr:
			return "NOT " + render(node.left)
		case queryAndExpr:
			return "(" + render(node.left) + " AND " + render(node.right) + ")"
		case queryOrExpr:
			return "(" + render(node.left) + " OR " + render(node.right) + ")"
		default:
			return ""
		}
	}
	parts := make([]string, 0, len(filters.tools)+len(filters.cwds)+len(filters.afters)+1)
	for _, value := range filters.tools {
		parts = append(parts, "tool:"+value)
	}
	for _, value := range filters.cwds {
		parts = append(parts, "cwd:"+value)
	}
	for _, value := range filters.afters {
		parts = append(parts, "after:"+value)
	}
	if value := render(expr); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " "), nil
}
