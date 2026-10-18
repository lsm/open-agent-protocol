use std::ops::Range;

use ra_ap_rustc_lexer::{strip_shebang, tokenize, FrontmatterAllowed, TokenKind};

#[derive(Debug, PartialEq, Eq)]
pub enum ScanError {
    UnterminatedBlockComment(usize),
}

pub fn comments(src: &str) -> Result<Vec<Range<usize>>, ScanError> {
    let start = strip_shebang(src).unwrap_or(0);
    let mut spans = Vec::new();
    let mut at = start;
    for token in tokenize(&src[start..], FrontmatterAllowed::Yes) {
        let end = at + token.len as usize;
        match token.kind {
            TokenKind::LineComment { .. } => spans.push(at..end),
            TokenKind::BlockComment { terminated, .. } => {
                if !terminated {
                    return Err(ScanError::UnterminatedBlockComment(at));
                }
                spans.push(at..end);
            }
            _ => {}
        }
        at = end;
    }
    Ok(spans)
}

pub fn strip(src: &str) -> Result<String, ScanError> {
    let spans = comments(src)?;
    if spans.is_empty() {
        return Ok(src.to_owned());
    }
    let mut out = String::with_capacity(src.len());
    let mut touched = Vec::new();
    let mut from = 0;
    for span in &spans {
        out.push_str(&src[from..span.start]);
        let before = src[..span.start].chars().next_back();
        let after = src[span.end..].chars().next();
        if before.is_some_and(|c| !c.is_whitespace()) && after.is_some_and(|c| !c.is_whitespace()) {
            out.push(' ');
        }
        touched.push(out.matches('\n').count());
        from = span.end;
    }
    out.push_str(&src[from..]);
    let mut kept = String::with_capacity(out.len());
    for (index, line) in out.split_inclusive('\n').enumerate() {
        if !touched.contains(&index) {
            kept.push_str(line);
            continue;
        }
        let body = line.trim_end();
        if body.is_empty() {
            continue;
        }
        kept.push_str(body);
        if line.ends_with('\n') {
            kept.push('\n');
        }
    }
    Ok(kept)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn found(src: &str) -> Vec<&str> {
        comments(src)
            .unwrap()
            .into_iter()
            .map(|span| &src[span])
            .collect()
    }

    #[test]
    fn line_doc_inner_doc_and_nested_block_comments_are_all_comments() {
        let src = "//! crate\n/// item\nfn f() {} // tail\n/* outer /* inner */ still */\n";
        assert_eq!(
            found(src),
            [
                "//! crate",
                "/// item",
                "// tail",
                "/* outer /* inner */ still */"
            ]
        );
    }

    #[test]
    fn slashes_inside_strings_raw_strings_chars_and_lifetimes_are_not_comments() {
        let src = "fn f<'a>(x: &'a str) -> char {\n    let _ = \"// no\";\n    let _ = r#\"/* no */\"#;\n    let _ = b\"//\";\n    '/'\n}\n";
        assert!(found(src).is_empty());
    }

    #[test]
    fn a_shebang_is_not_a_comment_but_a_comment_after_it_is() {
        assert_eq!(found("#!/usr/bin/env run\n// c\nfn main() {}\n"), ["// c"]);
    }

    #[test]
    fn an_attribute_is_not_a_comment() {
        assert!(
            found("#![doc = include_str!(\"../README.md\")]\n#[rustfmt::skip]\nfn f() {}\n")
                .is_empty()
        );
    }

    #[test]
    fn strip_drops_comment_only_lines_and_trailing_comments() {
        let src = "/// doc\nfn f() {\n    // gone\n    let x = 1; // tail\n    /* a\n       b */\n    x\n}\n";
        assert_eq!(strip(src).unwrap(), "fn f() {\n    let x = 1;\n    x\n}\n");
    }

    #[test]
    fn strip_keeps_tokens_a_block_comment_separated_apart() {
        assert_eq!(strip("let a = b/**/+c;\n").unwrap(), "let a = b +c;\n");
        assert_eq!(strip("let x/* t */= 1;\n").unwrap(), "let x = 1;\n");
    }

    #[test]
    fn strip_leaves_blank_lines_no_comment_touched() {
        let src = "fn a() {}\n\nfn b() {}\n";
        assert_eq!(strip(src).unwrap(), src);
    }

    #[test]
    fn an_unterminated_block_comment_is_refused() {
        assert_eq!(
            comments("fn f() {} /* open"),
            Err(ScanError::UnterminatedBlockComment(10))
        );
    }
}
