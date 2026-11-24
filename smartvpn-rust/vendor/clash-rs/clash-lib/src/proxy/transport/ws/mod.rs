use async_trait::async_trait;
use http::{Request, StatusCode};
use percent_encoding::{AsciiSet, CONTROLS, utf8_percent_encode};
use std::collections::HashMap;
use tokio_tungstenite::{
    client_async_with_config,
    tungstenite::{handshake::client::generate_key, protocol::WebSocketConfig},
};

use super::Transport;
use crate::{common::errors::map_io_error, proxy::AnyStream};

mod websocket;
mod websocket_early_data;

pub use websocket::WebsocketConn;
pub use websocket_early_data::WebsocketEarlyDataConn;

const PATH_AND_QUERY_ENCODE_SET: &AsciiSet = &CONTROLS
    .add(b' ')
    .add(b'"')
    .add(b'#')
    .add(b'<')
    .add(b'>')
    .add(b'[')
    .add(b'\\')
    .add(b']')
    .add(b'^')
    .add(b'`')
    .add(b'{')
    .add(b'|')
    .add(b'}');

pub struct Client {
    server: String,
    port: u16,
    path: String,
    headers: HashMap<String, String>,
    ws_config: Option<WebSocketConfig>,
    max_early_data: usize,
    early_data_header_name: String,
    pub(crate) http_upgrade: bool,
}

impl Client {
    pub fn new(
        server: String,
        port: u16,
        path: String,
        headers: HashMap<String, String>,
        ws_config: Option<WebSocketConfig>,
        max_early_data: usize,
        early_data_header_name: String,
    ) -> Self {
        Self {
            server,
            port,
            path,
            headers,
            ws_config,
            max_early_data,
            early_data_header_name,
            http_upgrade: false,
        }
    }

    fn req(&self) -> std::io::Result<Request<()>> {
        let path = if self.path.is_empty() {
            "/"
        } else {
            &self.path
        };
        let path = utf8_percent_encode(path, PATH_AND_QUERY_ENCODE_SET);
        let host = match self.server.parse::<std::net::Ipv6Addr>() {
            Ok(ip) => format!("[{ip}]"),
            Err(_) => self.server.clone(),
        };
        let authority = format!("{host}:{}", self.port);
        let mut request = Request::builder()
            .method("GET")
            .header("Connection", "Upgrade")
            .header("Upgrade", "websocket")
            .header("Sec-WebSocket-Version", "13")
            .header("Sec-WebSocket-Key", generate_key())
            .uri(format!("ws://{authority}{path}"));
        if !self.headers.keys().any(|k| k.eq_ignore_ascii_case("host")) {
            request = request.header("Host", authority);
        }
        for (k, v) in self.headers.iter() {
            request = request.header(k.as_str(), v.as_str());
        }
        if self.max_early_data > 0 && !self.early_data_header_name.is_empty() {
            // we will replace this field later
            request = request.header(self.early_data_header_name.as_str(), "xxoo");
        }
        request
            .body(())
            .map_err(|error| std::io::Error::new(std::io::ErrorKind::InvalidInput, error))
    }
}

#[async_trait]
impl Transport for Client {
    async fn proxy_stream(&self, stream: AnyStream) -> std::io::Result<AnyStream> {
        let mut req = self.req()?;
        if self.http_upgrade {
            req.headers_mut().remove("Sec-WebSocket-Version");
            req.headers_mut().remove("Sec-WebSocket-Key");
            return upgrade_stream(req, stream, false).await;
        }
        if self.max_early_data > 0 {
            let early_data_conn = WebsocketEarlyDataConn::new(
                stream,
                req,
                self.ws_config,
                self.early_data_header_name.clone(),
                self.max_early_data,
            );
            Ok(Box::new(early_data_conn))
        } else {
            let (stream, resp) = client_async_with_config(req, stream, self.ws_config)
                .await
                .map_err(map_io_error)?;

            if resp.status() != StatusCode::SWITCHING_PROTOCOLS {
                return Err(std::io::Error::new(
                    std::io::ErrorKind::InvalidData,
                    "invalid response",
                ));
            }
            Ok(Box::new(WebsocketConn::from_websocket(stream)))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{Client, Transport};
    use std::{collections::HashMap, sync::Arc};
    use tokio::io::duplex;
    use tokio_tungstenite::{
        accept_hdr_async,
        tungstenite::handshake::server::{Request, Response},
    };

    #[tokio::test]
    #[allow(clippy::result_large_err)]
    async fn websocket_request_target_is_encoded_without_normalization() {
        let client = Client::new(
            "example.com".to_owned(),
            443,
            "/a/./b/../already%20encoded/中文 path?ed=2048&name=中文%20value".to_owned(),
            HashMap::from([("Host".to_owned(), "example.com".to_owned())]),
            None,
            0,
            String::new(),
        );
        let (client_stream, server_stream) = duplex(4096);
        let request_target = Arc::new(std::sync::Mutex::new(None));
        let captured_target = Arc::clone(&request_target);

        let client_task =
            tokio::spawn(async move { client.proxy_stream(Box::new(client_stream)).await });
        let server_task = accept_hdr_async(
            server_stream,
            move |request: &Request, response: Response| {
                *captured_target.lock().expect("request target lock") =
                    request.uri().path_and_query().map(ToString::to_string);
                Ok(response)
            },
        );

        let (client_result, server_result) = tokio::join!(client_task, server_task);

        assert_eq!(
            request_target
                .lock()
                .expect("request target lock")
                .as_deref(),
            Some(
                "/a/./b/../already%20encoded/%E4%B8%AD%E6%96%87%20path?ed=2048&\
                 name=%E4%B8%AD%E6%96%87%20value"
            )
        );
        client_result
            .expect("WebSocket client task should not panic")
            .expect("WebSocket client handshake should succeed");
        server_result.expect("WebSocket server handshake should succeed");
    }

    async fn assert_invalid_input_without_panic(client: Client) {
        let (client_stream, server_stream) = duplex(1024);
        drop(server_stream);

        let result =
            tokio::spawn(async move { client.proxy_stream(Box::new(client_stream)).await })
                .await
                .expect("WebSocket client task should not panic");

        let error = match result {
            Ok(_) => panic!("invalid request should fail"),
            Err(error) => error,
        };
        assert_eq!(error.kind(), std::io::ErrorKind::InvalidInput);
    }

    #[tokio::test]
    async fn malformed_uri_returns_invalid_input() {
        let client = Client::new(
            "bad host".to_owned(),
            443,
            "/".to_owned(),
            HashMap::from([("Host".to_owned(), "example.com".to_owned())]),
            None,
            0,
            String::new(),
        );

        assert_invalid_input_without_panic(client).await;
    }

    #[tokio::test]
    async fn invalid_header_returns_invalid_input() {
        let client = Client::new(
            "example.com".to_owned(),
            443,
            "/".to_owned(),
            HashMap::from([("bad\nheader".to_owned(), "value".to_owned())]),
            None,
            0,
            String::new(),
        );

        assert_invalid_input_without_panic(client).await;
    }
}

/// The early-data subprotocol header carries payload. A peer can accept that
/// payload without negotiating a WebSocket subprotocol.
async fn upgrade_stream(
    mut req: Request<()>,
    stream: AnyStream,
    websocket: bool,
) -> std::io::Result<AnyStream> {
    use http_body_util::Empty;
    use hyper_util::rt::TokioIo;
    use tokio_tungstenite::tungstenite::handshake::derive_accept_key;

    let expected = if websocket {
        let key = req
            .headers()
            .get("Sec-WebSocket-Key")
            .expect("generated WebSocket key");
        Some(derive_accept_key(key.as_bytes()))
    } else {
        None
    };
    let protocol = req.headers().get("Sec-WebSocket-Protocol").cloned();
    *req.uri_mut() = req
        .uri()
        .path_and_query()
        .expect("request has path")
        .as_str()
        .parse()
        .map_err(map_io_error)?;
    let (mut sender, connection) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
        .await
        .map_err(map_io_error)?;
    tokio::spawn(async move {
        if let Err(e) = connection.with_upgrades().await {
            tracing::debug!("HTTP upgrade connection failed: {e}");
        }
    });
    let response = sender
        .send_request(req.map(|_| Empty::<bytes::Bytes>::new()))
        .await
        .map_err(map_io_error)?;
    let headers = response.headers();
    let token = |name: &str, token: &str| {
        headers
            .get_all(name)
            .iter()
            .flat_map(|v| v.as_bytes().split(|byte| *byte == b','))
            .any(|v| v.trim_ascii().eq_ignore_ascii_case(token.as_bytes()))
    };
    if response.status() != StatusCode::SWITCHING_PROTOCOLS
        || !token("connection", "upgrade")
        || !token("upgrade", "websocket")
    {
        return Err(std::io::Error::other(format!(
            "HTTP upgrade returned {}",
            response.status()
        )));
    }
    if let Some(expected) = expected {
        if headers.contains_key("Sec-WebSocket-Extensions")
            || headers.get("Sec-WebSocket-Accept").map(|v| v.as_bytes())
                != Some(expected.as_bytes())
            || headers
                .get("Sec-WebSocket-Protocol")
                .is_some_and(|v| Some(v) != protocol.as_ref())
        {
            return Err(std::io::Error::other(
                "invalid WebSocket handshake response",
            ));
        }
    }
    let upgraded = hyper::upgrade::on(response).await.map_err(map_io_error)?;
    let parts = upgraded
        .downcast::<TokioIo<AnyStream>>()
        .map_err(|_| std::io::Error::other("unexpected HTTP upgrade stream type"))?;
    let (reader, writer) = tokio::io::split(parts.io.into_inner());
    let reader = tokio::io::AsyncReadExt::chain(std::io::Cursor::new(parts.read_buf), reader);
    Ok(Box::new(tokio::io::join(reader, writer)))
}

type UpgradedReader =
    tokio::io::Chain<std::io::Cursor<bytes::Bytes>, tokio::io::ReadHalf<AnyStream>>;
impl crate::proxy::ProxyStream
    for tokio::io::Join<UpgradedReader, tokio::io::WriteHalf<AnyStream>>
{
}
