use regex::{Regex, RegexBuilder};
use regex_syntax::ast::{Ast, RepetitionKind, RepetitionRange};

const UNICODE_CLASSES: &str = concat!(
    "Adlam Ahom Anatolian_Hieroglyphs Any Arabic Armenian Avestan Balinese Bamum Bassa_Vah Batak Bengali ",
    "Beria_Erfe Bhaiksuki Bopomofo Brahmi Braille Buginese Buhid C Canadian_Aboriginal Carian ",
    "Caucasian_Albanian Cc Cf Chakma Cham Cherokee Chorasmian Cn Co Common Coptic Cs Cuneiform Cypriot ",
    "Cypro_Minoan Cyrillic Deseret Devanagari Dives_Akuru Dogra Duployan Egyptian_Hieroglyphs Elbasan ",
    "Elymaic Ethiopic Garay Georgian Glagolitic Gothic Grantha Greek Gujarati Gunjala_Gondi Gurmukhi ",
    "Gurung_Khema Han Hangul Hanifi_Rohingya Hanunoo Hatran Hebrew Hiragana Imperial_Aramaic Inherited ",
    "Inscriptional_Pahlavi Inscriptional_Parthian Javanese Kaithi Kannada Katakana Kawi Kayah_Li ",
    "Kharoshthi Khitan_Small_Script Khmer Khojki Khudawadi Kirat_Rai L LC Lao Latin Lepcha Limbu Linear_A ",
    "Linear_B Lisu Ll Lm Lo Lt Lu Lycian Lydian M Mahajani Makasar Malayalam Mandaic Manichaean Marchen ",
    "Masaram_Gondi Mc Me Medefaidrin Meetei_Mayek Mende_Kikakui Meroitic_Cursive Meroitic_Hieroglyphs ",
    "Miao Mn Modi Mongolian Mro Multani Myanmar N Nabataean Nag_Mundari Nandinagari Nd New_Tai_Lue Newa ",
    "Nko Nl No Nushu Nyiakeng_Puachue_Hmong Ogham Ol_Chiki Ol_Onal Old_Hungarian Old_Italic ",
    "Old_North_Arabian Old_Permic Old_Persian Old_Sogdian Old_South_Arabian Old_Turkic Old_Uyghur Oriya ",
    "Osage Osmanya P Pahawh_Hmong Palmyrene Pau_Cin_Hau Pc Pd Pe Pf Phags_Pa Phoenician Pi Po Ps ",
    "Psalter_Pahlavi Rejang Runic S Samaritan Saurashtra Sc Sharada Shavian Siddham Sidetic SignWriting ",
    "Sinhala Sk Sm So Sogdian Sora_Sompeng Soyombo Sundanese Sunuwar Syloti_Nagri Syriac Tagalog Tagbanwa ",
    "Tai_Le Tai_Tham Tai_Viet Tai_Yo Takri Tamil Tangsa Tangut Telugu Thaana Thai Tibetan Tifinagh ",
    "Tirhuta Todhri Tolong_Siki Toto Tulu_Tigalari Ugaritic Vai Vithkuqi Wancho Warang_Citi Yezidi Yi Z ",
    "Zanabazar_Square Zl Zp Zs ",
);

const UNICODE_ALIASES: &str = concat!(
    "Cased_Letter=LC Close_Punctuation=Pe Combining_Mark=M Connector_Punctuation=Pc Control=Cc ",
    "Currency_Symbol=Sc Dash_Punctuation=Pd Decimal_Number=Nd Enclosing_Mark=Me Final_Punctuation=Pf ",
    "Format=Cf Initial_Punctuation=Pi Letter=L Letter_Number=Nl Line_Separator=Zl Lowercase_Letter=Ll ",
    "Mark=M Math_Symbol=Sm Modifier_Letter=Lm Modifier_Symbol=Sk Nonspacing_Mark=Mn Number=N ",
    "Open_Punctuation=Ps Other=C Other_Letter=Lo Other_Number=No Other_Punctuation=Po Other_Symbol=So ",
    "Paragraph_Separator=Zp Private_Use=Co Punctuation=P Separator=Z Space_Separator=Zs Spacing_Mark=Mc ",
    "Surrogate=Cs Symbol=S Titlecase_Letter=Lt Unassigned=Cn Uppercase_Letter=Lu cntrl=Cc digit=Nd ",
    "punct=P ",
);

pub(super) struct Pattern(Regex);

fn assigned(c: char) -> bool {
    static ASSIGNED: std::sync::OnceLock<Regex> = std::sync::OnceLock::new();
    ASSIGNED
        .get_or_init(|| Regex::new(r"\A\p{Assigned}\z").expect("fixed Unicode property"))
        .is_match(c.encode_utf8(&mut [0; 4]))
}

impl Pattern {
    pub(super) fn matches(&self, name: &str) -> Option<bool> {
        name.chars().all(assigned).then(|| self.0.is_match(name))
    }
}

fn assigned_literals(ast: &Ast) -> bool {
    use regex_syntax::ast::{ClassSet, ClassSetItem};
    fn set(value: &ClassSet) -> bool {
        match value {
            ClassSet::BinaryOp(op) => set(&op.lhs) && set(&op.rhs),
            ClassSet::Item(value) => item(value),
        }
    }
    fn item(value: &ClassSetItem) -> bool {
        match value {
            ClassSetItem::Literal(literal) => assigned(literal.c),
            ClassSetItem::Range(range) => assigned(range.start.c) && assigned(range.end.c),
            ClassSetItem::Bracketed(class) => set(&class.kind),
            ClassSetItem::Union(union) => union.items.iter().all(item),
            _ => true,
        }
    }
    match ast {
        Ast::Literal(literal) => assigned(literal.c),
        Ast::ClassBracketed(class) => set(&class.kind),
        Ast::Group(group) => assigned_literals(&group.ast),
        Ast::Repetition(repetition) => assigned_literals(&repetition.ast),
        Ast::Concat(concat) => concat.asts.iter().all(assigned_literals),
        Ast::Alternation(alt) => alt.asts.iter().all(assigned_literals),
        _ => true,
    }
}

pub(super) fn compile(pattern: &str) -> Result<Option<Pattern>, ()> {
    let mut normalized = normalize(pattern)?;
    let ast = regex_syntax::ast::parse::ParserBuilder::new()
        .nest_limit(1000)
        .build()
        .parse(&normalized)
        .map_err(|_| ())?;
    validate(&ast, 1000)?;
    if !assigned_literals(&ast) || normalized.chars().any(|c| !assigned(c)) {
        return Ok(None);
    }
    boundaries(&ast, &mut normalized);
    match RegexBuilder::new(&format!(r"\A(?:{normalized})\z"))
        .nest_limit(1004)
        .size_limit(128 * 1024 * 1024)
        .build()
    {
        Ok(regex) => Ok(Some(Pattern(regex))),
        Err(regex::Error::CompiledTooBig(_)) => Ok(None),
        Err(regex::Error::Syntax(message))
            if message.contains("Unicode property") && message.contains("not found") =>
        {
            Ok(None)
        }
        Err(_) => Err(()),
    }
}

fn validate(ast: &Ast, limit: u32) -> Result<(), ()> {
    match ast {
        Ast::Group(g) => validate(&g.ast, limit)?,
        Ast::Repetition(r) => {
            let count = match r.op.kind {
                RepetitionKind::Range(
                    RepetitionRange::Exactly(n)
                    | RepetitionRange::AtLeast(n)
                    | RepetitionRange::Bounded(_, n),
                ) => n,
                _ => 1,
            };
            if matches!(*r.ast, Ast::Repetition(_)) {
                return Err(());
            }
            if count > limit {
                return Err(());
            }
            validate(&r.ast, limit.checked_div(count).unwrap_or(1000))?;
        }
        Ast::Concat(c) => {
            for a in &c.asts {
                validate(a, limit)?;
            }
        }
        Ast::Alternation(a) => {
            for a in &a.asts {
                validate(a, limit)?;
            }
        }
        _ => {}
    }
    Ok(())
}

fn boundaries(ast: &Ast, pattern: &mut String) {
    use regex_syntax::ast::AssertionKind;
    match ast {
        Ast::Assertion(a)
            if matches!(
                a.kind,
                AssertionKind::WordBoundary | AssertionKind::NotWordBoundary
            ) =>
        {
            let replacement = if a.kind == AssertionKind::WordBoundary {
                r"(?-u:\b)"
            } else {
                r"(?-u:\B)"
            };
            pattern.replace_range(a.span.start.offset..a.span.end.offset, replacement);
        }
        Ast::Group(g) => boundaries(&g.ast, pattern),
        Ast::Repetition(r) => boundaries(&r.ast, pattern),
        Ast::Concat(c) => {
            for a in c.asts.iter().rev() {
                boundaries(a, pattern);
            }
        }
        Ast::Alternation(a) => {
            for a in a.asts.iter().rev() {
                boundaries(a, pattern);
            }
        }
        _ => {}
    }
}

fn counted(mut chars: std::iter::Peekable<std::str::Chars<'_>>) -> bool {
    fn decimal(chars: &mut std::iter::Peekable<std::str::Chars<'_>>) -> bool {
        let Some(first) = chars.next().filter(char::is_ascii_digit) else {
            return false;
        };
        if first == '0' && chars.peek().is_some_and(char::is_ascii_digit) {
            return false;
        }
        while chars.peek().is_some_and(char::is_ascii_digit) {
            chars.next();
        }
        true
    }
    if !decimal(&mut chars) {
        return false;
    }
    if chars.peek() == Some(&',') {
        chars.next();
        if chars.peek() != Some(&'}') && !decimal(&mut chars) {
            return false;
        }
    }
    chars.next() == Some('}')
}

enum ClassAtom {
    Rune(char),
    Set(String),
}

fn perl_class(c: char) -> Option<&'static str> {
    match c {
        'd' => Some("[0-9]"),
        'D' => Some("[^0-9]"),
        'w' => Some("[A-Za-z0-9_]"),
        'W' => Some("[^A-Za-z0-9_]"),
        's' => Some("[\\t\\n\\f\\r ]"),
        'S' => Some("[^\\t\\n\\f\\r ]"),
        _ => None,
    }
}

fn class_atom(
    chars: &mut std::iter::Peekable<std::str::Chars<'_>>,
    sets: bool,
) -> Result<ClassAtom, ()> {
    let c = chars.next().ok_or(())?;
    if c == '[' && sets && chars.peek() == Some(&':') {
        let mut ascii = String::new();
        for c in chars.by_ref() {
            ascii.push(c);
            if c == ']' {
                break;
            }
        }
        let name = ascii
            .strip_prefix(':')
            .and_then(|s| s.strip_suffix(":]"))
            .ok_or(())?
            .trim_start_matches('^');
        if ![
            "alnum", "alpha", "ascii", "blank", "cntrl", "digit", "graph", "lower", "print",
            "punct", "space", "upper", "word", "xdigit",
        ]
        .contains(&name)
        {
            return Err(());
        }
        return Ok(ClassAtom::Set(format!("[{ascii}")));
    }
    if c != '\\' {
        return Ok(ClassAtom::Rune(c));
    }
    let escaped = chars.next().ok_or(())?;
    if sets {
        if let Some(class) = perl_class(escaped) {
            return Ok(ClassAtom::Set(class.to_owned()));
        }
        if matches!(escaped, 'p' | 'P') {
            let mut expression = format!("\\{escaped}");
            if chars.peek() == Some(&'{') {
                expression.push(chars.next().ok_or(())?);
                let mut closed = false;
                for c in chars.by_ref() {
                    expression.push(c);
                    if c == '}' {
                        closed = true;
                        break;
                    }
                }
                if !closed {
                    return Err(());
                }
            } else {
                expression.push(chars.next().ok_or(())?);
            }
            return Ok(ClassAtom::Set(normalize(&expression)?));
        }
    }
    let rune = match escaped {
        'a' => '\u{7}',
        'f' => '\u{c}',
        't' => '\t',
        'n' => '\n',
        'r' => '\r',
        'v' => '\u{b}',
        '0'..='7' => {
            let mut digits = escaped.to_string();
            while digits.len() < 3 && chars.peek().is_some_and(|c| ('0'..='7').contains(c)) {
                digits.push(chars.next().ok_or(())?);
            }
            if digits.len() == 1 && escaped != '0' {
                return Err(());
            }
            char::from_u32(u32::from_str_radix(&digits, 8).map_err(|_| ())?).ok_or(())?
        }
        'x' => {
            let mut digits = String::new();
            if chars.peek() == Some(&'{') {
                chars.next();
                let mut closed = false;
                for c in chars.by_ref() {
                    if c == '}' {
                        closed = true;
                        break;
                    }
                    digits.push(c);
                }
                if !closed {
                    return Err(());
                }
            } else {
                digits.push(chars.next().ok_or(())?);
                digits.push(chars.next().ok_or(())?);
            }
            if !digits.bytes().all(|digit| digit.is_ascii_hexdigit()) {
                return Err(());
            }
            char::from_u32(u32::from_str_radix(&digits, 16).map_err(|_| ())?).ok_or(())?
        }
        c if !c.is_ascii_alphanumeric() => c,
        _ => return Err(()),
    };
    Ok(ClassAtom::Rune(rune))
}

fn character_class(chars: &mut std::iter::Peekable<std::str::Chars<'_>>) -> Result<String, ()> {
    let mut result = String::from("[");
    if chars.peek() == Some(&'^') {
        result.push('^');
        chars.next();
    }
    let mut first = true;
    loop {
        if chars.peek() == Some(&']') && !first {
            chars.next();
            result.push(']');
            return Ok(result);
        }
        first = false;
        match class_atom(chars, true)? {
            ClassAtom::Set(set) => result.push_str(&set),
            ClassAtom::Rune(left) => {
                let mut lookahead = chars.clone();
                let range = lookahead.next() == Some('-') && lookahead.peek() != Some(&']');
                if range {
                    chars.next();
                    let ClassAtom::Rune(right) = class_atom(chars, false)? else {
                        return Err(());
                    };
                    if right < left {
                        return Err(());
                    }
                    result.push_str(&format!(
                        r"\x{{{:x}}}-\x{{{:x}}}",
                        left as u32, right as u32
                    ));
                } else {
                    result.push_str(&format!(r"\x{{{:x}}}", left as u32));
                }
            }
        }
    }
}

fn unicode_name(name: &str) -> Option<&'static str> {
    let canonical = |s: &str| {
        s.chars()
            .filter(|c| !matches!(c, '_' | '-' | ' '))
            .map(|c| c.to_ascii_lowercase())
            .collect::<String>()
    };
    let name = canonical(name);
    match name.as_str() {
        "ascii" => return Some("ASCII"),
        "assigned" => return Some("Assigned"),
        _ => {}
    }
    UNICODE_CLASSES
        .split_whitespace()
        .find(|known| canonical(known) == name)
        .or_else(|| {
            UNICODE_ALIASES
                .split_whitespace()
                .filter_map(|pair| pair.split_once('='))
                .find(|(alias, _)| canonical(alias) == name)
                .map(|(_, value)| value)
        })
}

fn normalize(pattern: &str) -> Result<String, ()> {
    let mut result = String::new();
    let mut chars = pattern.chars().peekable();
    let mut captures = 0;
    while let Some(c) = chars.next() {
        if c == '\\' {
            let escaped = chars.next().ok_or(())?;
            let replacement = perl_class(escaped);
            if escaped.is_ascii_digit() {
                if escaped > '7' {
                    return Err(());
                }
                let mut octal = escaped.to_string();
                while octal.len() < 3 && chars.peek().is_some_and(|c| ('0'..='7').contains(c)) {
                    octal.push(chars.next().ok_or(())?);
                }
                if octal.len() == 1 && escaped != '0' {
                    return Err(());
                }
                let value = u32::from_str_radix(&octal, 8).map_err(|_| ())?;
                result.push_str(&format!(r"\x{{{value:x}}}"));
            } else if let Some(replacement) = replacement {
                result.push_str(replacement);
            } else if matches!(escaped, 'p' | 'P' | 'x') && chars.peek() == Some(&'{') {
                chars.next();
                let mut value = String::new();
                let mut closed = false;
                for c in chars.by_ref() {
                    if c == '}' {
                        closed = true;
                        break;
                    }
                    value.push(c);
                }
                if !closed {
                    return Err(());
                }
                let mut kind = escaped;
                if escaped != 'x' {
                    if let Some(name) = value.strip_prefix('^') {
                        kind = if escaped == 'p' { 'P' } else { 'p' };
                        value = name.to_owned();
                    }
                    let name = unicode_name(&value).ok_or(())?;
                    if name == "Assigned" {
                        kind = if kind == 'p' { 'P' } else { 'p' };
                        value = "Cn".to_owned();
                    } else {
                        value = name.to_owned();
                    }
                }
                result.push('\\');
                result.push(kind);
                result.push('{');
                result.push_str(&value);
                result.push('}');
            } else if escaped == 'Q' {
                let mut quoted = String::new();
                while let Some(c) = chars.next() {
                    if c == '\\' && chars.peek() == Some(&'E') {
                        chars.next();
                        break;
                    }
                    quoted.push(c);
                }
                result.push_str(&regex::escape(&quoted));
            } else {
                if escaped.is_ascii_alphabetic() && !"aftnrvAbBzdDsSwWpPx".contains(escaped) {
                    return Err(());
                }
                if escaped.is_ascii_alphanumeric() {
                    result.push('\\');
                    result.push(escaped);
                } else {
                    result.push_str(&regex::escape(&escaped.to_string()));
                }
            }
        } else if c == '(' && chars.peek() == Some(&'?') {
            chars.next();
            if chars.peek() == Some(&':') {
                chars.next();
                result.push_str("(?:");
            } else if matches!(chars.peek(), Some('P' | '<')) {
                if chars.peek() == Some(&'P') {
                    chars.next();
                }
                if chars.next() != Some('<') {
                    return Err(());
                }
                let mut name = String::new();
                let mut closed = false;
                for c in chars.by_ref() {
                    if c == '>' {
                        closed = true;
                        break;
                    }
                    name.push(c);
                }
                if !closed
                    || name.is_empty()
                    || !name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_')
                {
                    return Err(());
                }
                captures += 1;
                result.push_str(&format!("(?P<oap_{captures}>"));
            } else {
                let mut enabled = true;
                let mut negative = false;
                let mut since_minus = false;
                let mut states = std::collections::BTreeMap::new();
                let end = loop {
                    let c = chars.next().ok_or(())?;
                    if c == ':' || c == ')' {
                        break c;
                    }
                    if c == '-' {
                        if negative {
                            return Err(());
                        }
                        negative = true;
                        enabled = false;
                    } else if "imsU".contains(c) {
                        states.insert(c, enabled);
                        if negative {
                            since_minus = true;
                        }
                    } else {
                        return Err(());
                    }
                };
                if negative && !since_minus {
                    return Err(());
                }
                if states.is_empty() && end == ')' {
                    continue;
                }
                result.push_str("(?");
                for (&c, &on) in &states {
                    if on {
                        result.push(c);
                    }
                }
                if states.values().any(|on| !on) {
                    result.push('-');
                    for (&c, &on) in &states {
                        if !on {
                            result.push(c);
                        }
                    }
                }
                result.push(end);
            }
        } else if c == '{' && !counted(chars.clone()) {
            result.push_str(r"\{");
        } else if c == '[' {
            result.push_str(&character_class(&mut chars)?);
        } else {
            result.push(c);
        }
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::compile;

    #[test]
    fn names_use_whole_name_matching_and_re2_character_classes() {
        for (pattern, name, expected) in [
            ("[a-z]+", "echo-1", false),
            ("a{01}", "a{01}", true),
            ("a{01}", "a", false),
            ("a{,3}", "a{,3}", true),
            (r"\bé\b", "é", false),
            ("ab|cd", "cd", true),
            ("(?i)[a-z]+", "ECHO", true),
            ("[a-z]+", "é", false),
            (r"\w+", "é", false),
            (r"\d+", "١", false),
            (r"\s", "\u{b}", false),
            (r"\s", "\t", true),
            (r"\s", "t", false),
            ("[^^]+", "abc", true),
            ("[^^]+", "a^", false),
            (r"[\d-a]+", "-a1", true),
            ("[--a]+", "0-a", true),
            (r"\Qecho.+\E", "echo.+", true),
            ("[a&&b]+", "a&b", true),
            ("[[:alpha:]]+", "echo", true),
        ] {
            assert_eq!(
                compile(pattern).unwrap().unwrap().matches(name),
                Some(expected),
                "{pattern} {name}"
            );
        }
    }

    #[test]
    fn invalid_re2_limits_are_rejected() {
        for pattern in [
            "[a-z",
            "(?=a)",
            r"\1",
            "x{1001}",
            "(?:a{200}){50}",
            "(?x)a",
            r"\p{Script=Greek}",
            "(?u)a",
            r"\u0061",
            r"[\x{+41}]",
            r"[\x+4]",
        ] {
            assert!(compile(pattern).is_err(), "{pattern}");
        }
    }
    #[test]
    fn re2_syntax_boundaries_follow_the_shared_matcher_cases() {
        for pattern in [
            r"\000",
            r"\123",
            r"\12",
            "(?)a",
            "(?P<a>x)(?P<a>y)",
            r"\p{Letter}",
            "(?P<1>a)",
            "(?ii)a",
            "(?i)abc",
            "(?i:ab)c",
            r"\pL+",
            r"\p{Greek}",
            r"\p{^Greek}",
            "[[:alpha:]]",
            r"\bword",
            "^*a",
            "café",
            r"\x{100}",
            r"\Qa.b\E",
            "a{01}",
            "a{,3}",
        ] {
            assert!(compile(pattern).is_ok(), "valid RE2: {pattern}");
        }
        for pattern in [
            "[a-z",
            "(ab",
            "ab)",
            "*a",
            "a**",
            "[z-a]",
            "[a--b]",
            r"\q",
            "(?P<>x)",
            "a{2,1}",
            "x{1001}",
            "(?i)[a-",
            "(?=a)",
            "(?<!a)b",
            "[[:foo:]]",
            r"\1",
            r"\8",
            r"\C",
            r"[\b]",
            "(?i-)a",
            "(?P<é>a)",
            "{2}",
            "a|{2}",
            "({2})",
            "a*{2}",
            "a{2}{3}",
            "a{2}*",
            "(?:[a-z]{1000}){1000}",
        ] {
            assert!(compile(pattern).is_err(), "invalid RE2: {pattern}");
        }
    }
    #[test]
    fn known_unicode_categories_and_scripts_are_never_misdiagnosed_as_invalid() {
        for name in super::UNICODE_CLASSES.split_whitespace() {
            assert!(compile(&format!(r"\p{{{name}}}")).is_ok(), "{name}");
        }
    }
    #[test]
    fn valid_unicode17_data_is_unsupported_instead_of_invalid() {
        assert!(matches!(compile(r"\p{Beria_Erfe}+"), Ok(None)));
    }
}
