//! WebSocket transport wrapper over any `BoxedStream`.

use std::{
    io,
    pin::Pin,
    task::{Context, Poll},
};

use futures_util::{SinkExt, StreamExt};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_tungstenite::{
    client_async_with_config,
    tungstenite::{client::IntoClientRequest, Message},
    WebSocketStream,
};

use crate::dialer::BoxedStream;

#[derive(Clone, Debug)]
pub struct WsConfig {
    pub path: String,
    pub host: Option<String>,
}

impl Default for WsConfig {
    fn default() -> Self {
        Self {
            path: "/".to_string(),
            host: None,
        }
    }
}

/// Upgrades a connected stream to a WebSocket binary stream.
pub async fn connect_ws(
    stream: BoxedStream,
    server_host: &str,
    cfg: &WsConfig,
) -> io::Result<BoxedStream> {
    connect_ws_headers(stream, server_host, cfg, &http::HeaderMap::new()).await
}

pub(crate) async fn connect_ws_headers(
    stream: BoxedStream,
    server_host: &str,
    cfg: &WsConfig,
    headers: &http::HeaderMap,
) -> io::Result<BoxedStream> {
    let host_header = cfg
        .host
        .as_deref()
        .filter(|h| !h.is_empty())
        .unwrap_or(server_host);
    let path = if cfg.path.starts_with('/') {
        cfg.path.as_str()
    } else {
        "/"
    };
    let uri = format!("ws://{host_header}{path}");
    let mut req = uri
        .into_client_request()
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?;
    req.headers_mut().insert(
        "Host",
        host_header
            .parse()
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?,
    );

    req.headers_mut().extend(headers.clone());
    let (ws, _) = client_async_with_config(req, stream, None)
        .await
        .map_err(|e| io::Error::new(io::ErrorKind::ConnectionAborted, e))?;
    Ok(Box::new(WsByteStream {
        inner: ws,
        read_buf: Vec::new(),
        read_pos: 0,
        eof: false,
    }) as BoxedStream)
}

struct WsByteStream {
    inner: WebSocketStream<BoxedStream>,
    read_buf: Vec<u8>,
    read_pos: usize,
    eof: bool,
}

impl AsyncRead for WsByteStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            if self.read_pos < self.read_buf.len() {
                let avail = &self.read_buf[self.read_pos..];
                let n = avail.len().min(out.remaining());
                out.put_slice(&avail[..n]);
                self.read_pos += n;
                if self.read_pos == self.read_buf.len() {
                    self.read_buf.clear();
                    self.read_pos = 0;
                }
                return Poll::Ready(Ok(()));
            }
            if self.eof {
                return Poll::Ready(Ok(()));
            }
            match self.inner.poll_next_unpin(cx) {
                Poll::Ready(Some(Ok(Message::Binary(data)))) => {
                    self.read_buf = data.to_vec();
                    self.read_pos = 0;
                }
                Poll::Ready(Some(Ok(Message::Text(text)))) => {
                    self.read_buf = text.as_bytes().to_vec();
                    self.read_pos = 0;
                }
                Poll::Ready(Some(Ok(Message::Close(_)))) | Poll::Ready(None) => {
                    self.eof = true;
                    return Poll::Ready(Ok(()));
                }
                Poll::Ready(Some(Ok(Message::Ping(_))))
                | Poll::Ready(Some(Ok(Message::Pong(_))))
                | Poll::Ready(Some(Ok(Message::Frame(_)))) => {}
                Poll::Ready(Some(Err(e))) => {
                    return Poll::Ready(Err(io::Error::new(io::ErrorKind::ConnectionAborted, e)));
                }
                Poll::Pending => return Poll::Pending,
            }
        }
    }
}

impl AsyncWrite for WsByteStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        match self.inner.poll_ready_unpin(cx) {
            Poll::Ready(Ok(())) => {
                let data = bytes::Bytes::copy_from_slice(buf);
                self.inner
                    .start_send_unpin(Message::Binary(data))
                    .map_err(|e| io::Error::new(io::ErrorKind::BrokenPipe, e))?;
                Poll::Ready(Ok(buf.len()))
            }
            Poll::Ready(Err(e)) => Poll::Ready(Err(io::Error::new(io::ErrorKind::BrokenPipe, e))),
            Poll::Pending => Poll::Pending,
        }
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.inner
            .poll_flush_unpin(cx)
            .map_err(|e| io::Error::new(io::ErrorKind::BrokenPipe, e))
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.inner
            .poll_close_unpin(cx)
            .map_err(|e| io::Error::new(io::ErrorKind::BrokenPipe, e))
    }
}
