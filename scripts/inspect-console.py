"""Drive the running console window over CDP.

Develop.md's UI convention: start KSpeech with KSPEECH_WEBVIEW_DEBUG_PORT, feed
the window a fake snapshot, then measure the DOM instead of eyeballing it. This
script pushes one assistant answer that cites a source — the long unbreakable
URL is what pushed the card out of its pane — and reports whether anything still
overflows, plus a screenshot to look at.

    python scripts/inspect-console.py <port> <screenshot.png>
"""

import base64
import json
import sys
import urllib.request

import websocket

ANSWER = (
    "他说的是：DeepSeek 刚发布新模型，目前以插件为核心，功能主要通过插件对接。"
    "([reddit.com](https://www.reddit.com/r/DeepSeek/comments/1abcdefg/"
    "deepseek_new_model_release_discussion_thread/?utm_source=openai&utm_medium=referral))"
)

SNAPSHOT = {
    "status": "stopped",
    "runningSeconds": 0,
    "text": "",
    "channels": [],
    "locked": False,
    "history": [],
    "config": {},
    "audioSources": [],
    "recognizers": [],
    "resources": [],
    "punctuationModels": [],
    "assistant": {
        "enabled": True,
        "configured": True,
        "summarize": True,
        "autoAnswer": True,
        "model": "gpt-5.6-luna",
        "tools": ["联网搜索"],
        "summarizing": False,
        "answering": False,
        "pendingLines": 0,
        "insights": [],
        "conversations": [
            {
                "id": 1,
                "time": "2026-08-14T14:21:40+08:00",
                "question": "说的什么",
                "answer": ANSWER,
                "source": "manual",
                "status": "ready",
            }
        ],
        "threadDigest": "DeepSeek 发布新模型；插件为核心",
        "digestedTurns": 3,
    },
    "version": "dev",
    "commit": "",
    "platform": "windows/amd64",
}

MEASURE = """
(() => {
  const list = document.querySelector('.chat-list');
  const row = document.querySelector('.qa-row');
  const pane = document.querySelector('.chat-pane');
  const answer = document.querySelector('.qa-answer');
  if (!list || !row || !pane || !answer) return JSON.stringify({error: 'chat pane not rendered'});
  const link = answer.querySelector('.qa-link');
  return JSON.stringify({
    listScrollWidth: list.scrollWidth,
    listClientWidth: list.clientWidth,
    rowRight: Math.round(row.getBoundingClientRect().right),
    paneRight: Math.round(pane.getBoundingClientRect().right),
    digestShown: Boolean(document.querySelector('.thread-digest')),
    linkText: link && link.textContent,
    linkTitle: link && link.getAttribute('title'),
    linkHasHref: Boolean(link && link.getAttribute('href')),
    // pre-wrap keeps every space the template leaks, so the text has to start
    // and end exactly where the answer does.
    answerText: JSON.stringify(answer.textContent),
  });
})()
"""


def target(port, title_fragment):
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=10) as response:
        pages = json.load(response)
    for page in pages:
        if title_fragment in page.get("url", "") or title_fragment in page.get("title", ""):
            return page["webSocketDebuggerUrl"]
    raise SystemExit(f"no target matching {title_fragment!r} in {[p.get('url') for p in pages]}")


class Session:
    def __init__(self, url):
        # WebView2 rejects a DevTools socket that carries an Origin header
        # unless the browser was launched with --remote-allow-origins.
        self.socket = websocket.create_connection(url, timeout=20, suppress_origin=True)
        self.next_id = 0

    def call(self, method, **params):
        self.next_id += 1
        request_id = self.next_id
        self.socket.send(json.dumps({"id": request_id, "method": method, "params": params}))
        while True:
            message = json.loads(self.socket.recv())
            if message.get("id") == request_id:
                if "error" in message:
                    raise SystemExit(f"{method} failed: {message['error']}")
                return message.get("result", {})

    def evaluate(self, expression):
        result = self.call("Runtime.evaluate", expression=expression, returnByValue=True, awaitPromise=True)
        if "exceptionDetails" in result:
            raise SystemExit(f"evaluate failed: {result['exceptionDetails']}")
        return result["result"].get("value")


def main():
    port = sys.argv[1] if len(sys.argv) > 1 else "9333"
    screenshot = sys.argv[2] if len(sys.argv) > 2 else "console.png"
    session = Session(target(port, "view=console"))
    session.call("Runtime.enable")
    session.evaluate(
        "window._wails.dispatchWailsEvent({name:'kspeech:state',data:"
        + json.dumps(SNAPSHOT, ensure_ascii=False)
        + "})"
    )
    session.evaluate("new Promise(resolve => setTimeout(resolve, 300))")
    print(session.evaluate(MEASURE))
    data = session.call("Page.captureScreenshot", format="png")["data"]
    with open(screenshot, "wb") as file:
        file.write(base64.b64decode(data))
    print(f"screenshot: {screenshot}")


if __name__ == "__main__":
    main()
