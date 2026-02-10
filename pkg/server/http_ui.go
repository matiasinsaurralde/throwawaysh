package server

const indexHTMLPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>throwawaysh console</title>
  <style>
    :root {
      --bg: #050806;
      --panel: #0b120d;
      --line: #1d4d2c;
      --green: #55ff85;
      --soft: #2bd968;
      --text: #c3ffd4;
      --warn: #ff6e6e;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: radial-gradient(circle at top, #0b160d 0%, var(--bg) 60%);
      color: var(--text);
      font-family: "JetBrains Mono", "Fira Code", "SFMono-Regular", Menlo, Consolas, monospace;
      height: 100vh;
      overflow: hidden;
    }
    .container {
      display: grid;
      grid-template-columns: 1fr 1fr;
      height: 100vh;
    }
    .left, .right {
      border: 1px solid var(--line);
      background: linear-gradient(180deg, rgba(10,20,12,0.92), rgba(4,8,5,0.96));
      margin: 10px;
      border-radius: 12px;
      position: relative;
      overflow: hidden;
    }
    .title {
      position: absolute;
      top: 8px;
      left: 12px;
      font-size: 13px;
      color: var(--soft);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      z-index: 20;
    }
    .universe {
      position: absolute;
      inset: 0;
      overflow: hidden;
    }
    .node {
      position: absolute;
      width: 16px;
      height: 16px;
      border-radius: 50%;
      background: radial-gradient(circle at 30% 30%, #9dffc2, #1eb857);
      border: 1px solid rgba(118,255,165,0.85);
      box-shadow: 0 0 18px rgba(63,255,141,0.7);
      cursor: pointer;
      transform-origin: center;
      opacity: 1;
      transition: box-shadow 160ms ease, border-color 160ms ease;
      animation: pulse 7s ease-in-out infinite;
    }
    .node.selected {
      box-shadow: 0 0 24px rgba(255,255,255,0.8), 0 0 30px rgba(63,255,141,1);
      border-color: #ffffff;
    }
    .node.entering {
      animation: node-enter 420ms ease-out forwards, pulse 7s ease-in-out 450ms infinite;
    }
    .node.exiting {
      animation: node-exit 420ms ease-in forwards;
      pointer-events: none;
    }
    .node-label {
      position: absolute;
      top: -22px;
      left: -6px;
      white-space: pre;
      font-size: 11px;
      color: #b7ffd2;
      background: rgba(0, 0, 0, 0.65);
      border: 1px solid rgba(87, 242, 146, 0.42);
      border-radius: 6px;
      padding: 2px 6px;
      pointer-events: none;
    }
    @keyframes pulse {
      0%   { transform: translate(0, 0); }
      50%  { transform: translate(0, -1px); }
      100% { transform: translate(0, 0); }
    }
    @keyframes node-enter {
      0% {
        opacity: 0;
        transform: scale(0.25);
        filter: blur(1.5px);
      }
      55% {
        opacity: 1;
        transform: scale(1.15);
        filter: blur(0);
      }
      100% {
        opacity: 1;
        transform: scale(1);
        filter: blur(0);
      }
    }
    @keyframes node-exit {
      0% {
        opacity: 1;
        transform: scale(1);
        filter: blur(0);
      }
      100% {
        opacity: 0;
        transform: scale(0.2);
        filter: blur(2px);
      }
    }
    .terminal {
      position: absolute;
      inset: 38px 10px 10px 10px;
      border: 1px solid rgba(87, 242, 146, 0.35);
      border-radius: 8px;
      background: rgba(0,0,0,0.55);
      padding: 10px;
      overflow: auto;
      white-space: pre-wrap;
      color: var(--green);
      font-size: 12px;
      line-height: 1.4;
    }
    .placeholder {
      color: #6eb98a;
      opacity: 0.8;
    }
    .status {
      position: absolute;
      top: 8px;
      right: 12px;
      font-size: 12px;
      color: #77d696;
      z-index: 20;
    }
    .status.error { color: var(--warn); }
  </style>
</head>
<body>
  <div class="container">
    <section class="left">
      <div class="title">Sessions</div>
      <div class="status" id="nodeStatus">0 active sessions</div>
      <div class="universe" id="universe"></div>
    </section>
    <section class="right">
      <div class="title" id="terminalTitle">Terminal Stream</div>
      <div class="status" id="terminalStatus">idle</div>
      <pre class="terminal" id="terminal"><span class="placeholder">Select a VM node to stream its session log...</span></pre>
    </section>
  </div>
  <script>
    const universeEl = document.getElementById('universe');
    const nodeStatusEl = document.getElementById('nodeStatus');
    const terminalEl = document.getElementById('terminal');
    const terminalStatusEl = document.getElementById('terminalStatus');
    const terminalTitleEl = document.getElementById('terminalTitle');

    const nodeState = new Map();
    let selectedSessionID = '';
    let stream = null;
    let physicsStarted = false;

    function randomBetween(min, max) {
      return min + Math.random() * (max - min);
    }

    function humanDuration(seconds) {
      if (seconds < 60) return seconds + 's';
      const mins = Math.floor(seconds / 60);
      const rem = seconds % 60;
      return mins + 'm ' + rem + 's';
    }

    function appendTerminal(chunk) {
      if (!chunk) return;
      if (terminalEl.querySelector('.placeholder')) {
        terminalEl.textContent = '';
      }
      terminalEl.textContent += chunk;
      if (terminalEl.textContent.length > 120000) {
        terminalEl.textContent = terminalEl.textContent.slice(-90000);
      }
      terminalEl.scrollTop = terminalEl.scrollHeight;
    }

    function resetTerminal(message) {
      if (stream) {
        stream.close();
        stream = null;
      }
      terminalEl.textContent = '';
      const span = document.createElement('span');
      span.className = 'placeholder';
      span.textContent = message || 'Select a VM node to stream its session log...';
      terminalEl.appendChild(span);
      terminalStatusEl.textContent = 'idle';
      terminalStatusEl.className = 'status';
      terminalTitleEl.textContent = 'Terminal Stream';
    }

    function openStream(session) {
      if (stream) {
        stream.close();
        stream = null;
      }
      terminalEl.textContent = '';
      terminalStatusEl.textContent = 'streaming';
      terminalStatusEl.className = 'status';
      terminalTitleEl.textContent = 'Terminal Stream [' + session.id + ']';

      stream = new EventSource('/api/sessions/' + encodeURIComponent(session.id) + '/stream');
      stream.addEventListener('chunk', (event) => {
        try {
          const payload = JSON.parse(event.data);
          appendTerminal(payload.chunk || '');
        } catch (_err) {
          appendTerminal(event.data || '');
        }
      });
      stream.addEventListener('end', () => {
        terminalStatusEl.textContent = 'session ended';
        selectedSessionID = '';
        if (stream) {
          stream.close();
          stream = null;
        }
      });
      stream.onerror = () => {
        terminalStatusEl.textContent = 'stream disconnected';
        terminalStatusEl.className = 'status error';
        if (stream) {
          stream.close();
          stream = null;
        }
      };
    }

    function createNodeState(session) {
      const node = document.createElement('div');
      node.className = 'node entering';
      node.dataset.sessionId = session.id;

      const label = document.createElement('div');
      label.className = 'node-label';
      node.appendChild(label);
      universeEl.appendChild(node);

      const state = {
        id: session.id,
        el: node,
        labelEl: label,
        x: randomBetween(6, 92),
        y: randomBetween(10, 88),
        vx: randomBetween(-0.006, 0.006),
        vy: randomBetween(-0.006, 0.006),
        driftX: randomBetween(-0.0025, 0.0025),
        driftY: randomBetween(-0.0025, 0.0025),
      };
      nodeState.set(session.id, state);

      node.addEventListener('click', () => {
        selectedSessionID = session.id;
        openStream(session);
      });
      setTimeout(() => {
        if (node.isConnected) {
          node.classList.remove('entering');
        }
      }, 460);
      return state;
    }

    function updateNodeLabel(state, session) {
      if (!state || !state.labelEl) return;
      const node = state.el;
      node.classList.toggle('selected', selectedSessionID === session.id);
      if (!node.style.animationDelay) {
        node.style.animationDelay = (Math.random() * 2).toFixed(2) + 's';
      }
      const flag = session.country_flag ? session.country_flag + ' ' : '';
      state.labelEl.textContent =
        flag + session.remote_ip + ' [' + humanDuration(session.duration_seconds) + ']\n' +
        'user: ' + (session.username || '<none>') + ' pass: ' + (session.password || '<none>');
    }

    function removeNode(sessionID) {
      const state = nodeState.get(sessionID);
      if (!state) return;
      const node = state.el;
      node.classList.add('exiting');
      node.classList.remove('selected');
      setTimeout(() => {
        if (node.parentNode) {
          node.parentNode.removeChild(node);
        }
        nodeState.delete(sessionID);
      }, 430);
    }

    function startPhysicsLoop() {
      if (physicsStarted) return;
      physicsStarted = true;
      const centerX = 50;
      const centerY = 50;
      const pull = 0.00008;
      const friction = 0.996;
      const maxSpeed = 0.04;
      const jitter = 0.00018;

      function step() {
        nodeState.forEach((state) => {
          if (!state.el.isConnected) return;
          const dx = centerX - state.x;
          const dy = centerY - state.y;
          state.vx += dx * pull + state.driftX + randomBetween(-jitter, jitter);
          state.vy += dy * pull + state.driftY + randomBetween(-jitter, jitter);
          state.vx *= friction;
          state.vy *= friction;

          if (state.vx > maxSpeed) state.vx = maxSpeed;
          if (state.vx < -maxSpeed) state.vx = -maxSpeed;
          if (state.vy > maxSpeed) state.vy = maxSpeed;
          if (state.vy < -maxSpeed) state.vy = -maxSpeed;

          state.x += state.vx;
          state.y += state.vy;

          if (state.x < 2) {
            state.x = 2;
            state.vx = Math.abs(state.vx) * 0.6;
          } else if (state.x > 97) {
            state.x = 97;
            state.vx = -Math.abs(state.vx) * 0.6;
          }
          if (state.y < 7) {
            state.y = 7;
            state.vy = Math.abs(state.vy) * 0.6;
          } else if (state.y > 93) {
            state.y = 93;
            state.vy = -Math.abs(state.vy) * 0.6;
          }

          state.el.style.left = state.x + '%';
          state.el.style.top = state.y + '%';
        });
        window.requestAnimationFrame(step);
      }
      window.requestAnimationFrame(step);
    }

    function renderNodes(sessions) {
      startPhysicsLoop();
      const activeIDs = new Set(sessions.map((session) => session.id));

      nodeState.forEach((_state, sessionID) => {
        if (!activeIDs.has(sessionID)) {
          removeNode(sessionID);
        }
      });

      sessions.forEach((session) => {
        let state = nodeState.get(session.id);
        if (!state) {
          state = createNodeState(session);
        }
        updateNodeLabel(state, session);
      });

      nodeStatusEl.textContent = sessions.length + ' active sessions';

      if (selectedSessionID && !activeIDs.has(selectedSessionID)) {
        selectedSessionID = '';
        resetTerminal('Session ended. Select another active node...');
      }
    }

    async function refreshSessions() {
      try {
        const response = await fetch('/api/sessions', { cache: 'no-store' });
        if (!response.ok) throw new Error('request failed');
        const payload = await response.json();
        const sessions = Array.isArray(payload.sessions) ? payload.sessions : [];
        renderNodes(sessions);
      } catch (_err) {
        nodeStatusEl.textContent = 'connection error';
      }
    }

    resetTerminal('');
    refreshSessions();
    setInterval(refreshSessions, 1200);
  </script>
</body>
</html>`
