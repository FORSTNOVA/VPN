use bytes::Bytes;
use http_body_util::{BodyExt, Full};
use hyper::Request;
use hyper_util::rt::TokioIo;
use serde_json::{json, Value};
use smartvpn_engine::{
    dialer::{BoxedStream, TargetAddr},
    router::{RouteResult, Router},
};
use std::{
    error::Error,
    io,
    sync::Arc,
    time::{Duration, Instant},
};
use tokio_rustls::{
    rustls::{pki_types::ServerName, ClientConfig, RootCertStore},
    TlsConnector,
};

type CheckError = Box<dyn Error + Send + Sync>;

pub(super) async fn request(
    router: &Router,
    url: &str,
    upload: Option<Bytes>,
    limit: usize,
    capture: bool,
) -> Result<(usize, Vec<u8>), CheckError> {
    tokio::time::timeout(Duration::from_secs(90), async {
        let uri: hyper::Uri = url.parse()?;
        let tls = match uri.scheme_str() {
            Some("https") => true,
            Some("http") => false,
            _ => return Err(io::Error::other("expected HTTP or HTTPS URL").into()),
        };
        let host = uri
            .host()
            .ok_or_else(|| io::Error::other("missing target host"))?;
        let target = TargetAddr::domain(host, uri.port_u16().unwrap_or(if tls { 443 } else { 80 }));
        let RouteResult::Connected { stream, .. } = router
            .dial_tcp(([127, 0, 0, 1], 0).into(), &target, None)
            .await
            .map_err(|e| io::Error::other(format!("建立代理连接到 {host}: {e}")))?
        else {
            return Err(io::Error::other("target rejected by routing policy").into());
        };
        let stream: BoxedStream = if tls {
            let roots = RootCertStore::from_iter(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
            let mut config = ClientConfig::builder()
                .with_root_certificates(roots)
                .with_no_client_auth();
            config.alpn_protocols = vec![b"http/1.1".to_vec()];
            Box::new(
                TlsConnector::from(Arc::new(config))
                    .connect(ServerName::try_from(host.to_owned())?, stream)
                    .await
                    .map_err(|e| io::Error::other(format!("{host} TLS 握手: {e}")))?,
            )
        } else {
            stream
        };
        let (mut sender, conn) =
            hyper::client::conn::http1::handshake(TokioIo::new(stream)).await?;
        tokio::spawn(async move {
            if let Err(error) = conn.await {
                eprintln!("route check HTTP connection: {error}");
            }
        });
        let req = Request::builder()
            .method(if upload.is_some() { "POST" } else { "GET" })
            .uri(uri.path_and_query().map_or("/", |path| path.as_str()))
            .header("Host", uri.authority().unwrap().as_str())
            .header("User-Agent", "SmartVPN/0.5")
            .header("Accept-Encoding", "identity")
            .body(Full::new(upload.unwrap_or_default()))?;
        let response = sender
            .send_request(req)
            .await
            .map_err(|e| io::Error::other(format!("读取 {host} HTTP 响应头: {e}")))?;
        if !response.status().is_success() {
            return Err(io::Error::other(format!("HTTP {}", response.status())).into());
        }
        let mut body = response.into_body();
        let mut count = 0;
        let mut data = Vec::new();
        while let Some(frame) = body.frame().await {
            if let Ok(chunk) = frame
                .map_err(|e| {
                    io::Error::other(format!("读取 {host} 响应正文（已收到 {count} 字节）: {e}"))
                })?
                .into_data()
            {
                count += chunk.len();
                if count > limit {
                    return Err(io::Error::other("response exceeds size limit").into());
                }
                if capture {
                    data.extend_from_slice(&chunk);
                }
            }
        }
        Ok((count, data))
    })
    .await?
}

pub(super) async fn speed(router: &Router, mb: usize) -> Result<Value, CheckError> {
    let down = mb * 1_000_000;
    let up = down / 3;
    let start = Instant::now();
    let (downloaded, _) = request(
        router,
        &format!("https://speed.cloudflare.com/__down?bytes={down}"),
        None,
        down,
        false,
    )
    .await?;
    if downloaded != down {
        return Err(io::Error::other("incomplete speed test download").into());
    }
    let down_time = start.elapsed();
    let payload = Bytes::from(vec![0; up]);
    let start = Instant::now();
    request(
        router,
        "https://speed.cloudflare.com/__up",
        Some(payload),
        65536,
        false,
    )
    .await?;
    let up_time = start.elapsed();
    Ok(
        json!({"speed":{"target":"Cloudflare","megaBytes":mb,"downBytes":down,"upBytes":up,
        "downMbps":8.0*down as f64/1e6/down_time.as_secs_f64(),"upMbps":8.0*up as f64/1e6/up_time.as_secs_f64(),
        "downMs":down_time.as_millis(),"upMs":up_time.as_millis()}}),
    )
}

pub(super) async fn site(router: &Router, name: &str, url: &str) -> Value {
    let start = Instant::now();
    match request(router, url, None, 8 * 1024 * 1024, false).await {
        Ok(_) => json!({"name":name,"state":"ok","latencyMs":start.elapsed().as_millis()}),
        Err(e) => {
            json!({"name":name,"state":"error","detail":e.to_string(),"latencyMs":start.elapsed().as_millis()})
        }
    }
}

pub(super) async fn egress(router: &Router) -> Result<std::net::IpAddr, CheckError> {
    let (_, data) = request(router, "https://api.ipify.org", None, 128, true).await?;
    Ok(std::str::from_utf8(&data)?.trim().parse()?)
}
