use anyhow::{anyhow, ensure, Result};
use std::{io::Read, time::Duration};

pub fn agent() -> ureq::Agent {
    ureq::AgentBuilder::new()
        .try_proxy_from_env(false)
        .timeout(Duration::from_secs(12))
        .redirects(0)
        .build()
}
pub fn download_agent() -> ureq::Agent {
    let builder = ureq::AgentBuilder::new()
        .try_proxy_from_env(false)
        .redirects(5)
        .user_agent("clash.meta")
        .timeout(Duration::from_secs(12));
    #[cfg(target_os = "android")]
    let builder = builder.resolver(crate::android_dns::resolve);
    builder.build()
}
pub fn body(resp: ureq::Response) -> Result<Vec<u8>> {
    const MAX: u64 = 8 * 1024 * 1024;
    let mut bytes = Vec::new();
    resp.into_reader()
        .take(MAX + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| anyhow!("HTTP response read failed ({:?})", e.kind()))?;
    ensure!(bytes.len() as u64 <= MAX, "response exceeds 8 MiB");
    Ok(bytes)
}
// Transport messages can embed the complete URL, including path credentials.
pub fn error(err: ureq::Error) -> anyhow::Error {
    match err {
        ureq::Error::Status(code, _) => anyhow!("upstream HTTP {code}"),
        ureq::Error::Transport(t) => anyhow!("upstream transport failed ({:?})", t.kind()),
    }
}

pub fn redact(message: &str) -> String {
    use std::sync::LazyLock;
    static PRIVATE: LazyLock<regex::Regex> = LazyLock::new(|| {
        regex::Regex::new(
        r#"(?ix)\b(?:[a-f\d]{8}(?:-[a-f\d]{4}){3}-[a-f\d]{12}|(?:\d{1,3}\.){3}\d{1,3})(?::\d+)?\b|\[?[a-f\d]{0,4}(?::[a-f\d]{0,4}){2,7}\]?(?::\d+)?|\b(?:bearer\s+|(?:password|token|secret|uuid)\s*[=:]\s*)[^\s,;]+"#
    ).unwrap()
    });
    let lower = message.to_ascii_lowercase();
    if let Some(pos) = lower.find("://") {
        let end = message[..pos]
            .rfind(|c: char| c.is_ascii_whitespace())
            .map_or(0, |p| p + 1);
        return format!(
            "{}[redacted URL]",
            PRIVATE.replace_all(&message[..end], "[redacted]")
        );
    }
    PRIVATE.replace_all(message, "[redacted]").into_owned()
}
