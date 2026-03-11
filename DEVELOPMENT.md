# AutoTo 開發指南

## 開發環境設定

### 1. 安裝開發依賴

```bash
cd autoto
source venv/bin/activate

# 安裝開發工具
pip install pytest black flake8 mypy

# 安裝台灣版額外依賴
pip install line-bot-sdk flask requests
```

### 2. 設定 Git Hooks

```bash
# 建立 pre-commit hook
cat > .git/hooks/pre-commit << 'EOF'
#!/bin/bash
# 執行程式碼格式化
black backend/
# 執行 linting
flake8 backend/
EOF

chmod +x .git/hooks/pre-commit
```

### 3. IDE 設定（VS Code）

```json
// .vscode/settings.json
{
  "python.linting.enabled": true,
  "python.linting.flake8Enabled": true,
  "python.formatting.provider": "black",
  "editor.formatOnSave": true,
  "python.testing.pytestEnabled": true
}
```

## 專案架構

### 核心概念

```
User Message → Channel → Gateway → Agent Loop → Tools → Response
```

1. **Channel**: 接收來自不同平台的訊息（LINE、Telegram 等）
2. **Gateway**: 路由訊息到對應的 agent
3. **Agent Loop**: 核心推理循環，決定要呼叫哪些工具
4. **Tools**: 執行具體任務（天氣查詢、發票對獎等）
5. **Response**: 回傳結果給用戶

### 目錄結構詳解

```
backend/
├── channels/            # 聊天平台整合
│   ├── gateway.py
│   ├── line_bot.py
│   └── ...
├── core/                # agent / config / scheduler / tools
│   ├── agent.py
│   ├── config.py
│   ├── memory.py
│   ├── scheduler.py
│   └── tools.py
├── requirements.txt
└── server.py
```

## 開發新功能

### 1. 開發新的 Channel

```python
# examples/channels/base.py

from abc import ABC, abstractmethod
from typing import Dict, Any

class BaseChannel(ABC):
    """Channel 基礎類別"""
    
    def __init__(self, config: Dict[str, Any]):
        self.config = config
        self.enabled = config.get("enabled", False)
        self.allow_from = config.get("allowFrom", [])
    
    @abstractmethod
    def send_message(self, user_id: str, message: str):
        """發送訊息給用戶"""
        pass
    
    @abstractmethod
    def receive_message(self) -> Dict[str, Any]:
        """接收用戶訊息"""
        pass
    
    def is_allowed(self, user_id: str) -> bool:
        """檢查用戶是否在白名單"""
        if not self.allow_from:
            return True
        return user_id in self.allow_from


# examples/channels/my_channel.py

from .base import BaseChannel

class MyChannel(BaseChannel):
    """我的聊天平台整合"""
    
    def __init__(self, config):
        super().__init__(config)
        self.api_token = config.get("apiToken")
        # 初始化 API client
    
    def send_message(self, user_id: str, message: str):
        """實作發送訊息"""
        # 呼叫平台 API
        pass
    
    def receive_message(self) -> Dict[str, Any]:
        """實作接收訊息"""
        # 從 webhook 或 polling 取得訊息
        pass
```

### 2. 開發新的 Tool

```python
# examples/tools/base.py

from abc import ABC, abstractmethod
from typing import Any, Dict

class BaseTool(ABC):
    """Tool 基礎類別"""
    
    def __init__(self, config: Dict[str, Any] = None):
        self.config = config or {}
    
    @abstractmethod
    def execute(self, **kwargs) -> Any:
        """執行工具"""
        pass
    
    @property
    @abstractmethod
    def name(self) -> str:
        """工具名稱"""
        pass
    
    @property
    @abstractmethod
    def description(self) -> str:
        """工具說明"""
        pass


# examples/tools/my_tool.py

from .base import BaseTool

class MyTool(BaseTool):
    """我的工具"""
    
    @property
    def name(self) -> str:
        return "my_tool"
    
    @property
    def description(self) -> str:
        return "這是我的工具，用來做某件事"
    
    def execute(self, param1: str, param2: int = 0) -> str:
        """
        執行工具
        
        Args:
            param1: 參數1說明
            param2: 參數2說明
        
        Returns:
            執行結果
        """
        # 實作邏輯
        result = f"處理 {param1} with {param2}"
        return result


# 註冊到 AutoTo agent
def register_my_tool(agent):
    """註冊工具到 agent"""
    
    tool = MyTool()
    
    @agent.tool(
        name=tool.name,
        description=tool.description
    )
    def my_tool_wrapper(**kwargs):
        return tool.execute(**kwargs)
```

### 3. 整合到 AutoTo

```python
# examples/app_setup.py

from core.agent import AgentLoop
from .tools.my_tool import register_my_tool
from .channels.my_channel import MyChannel

def init_autoto_components(config_mgr, memory_mgr):
    """初始化 AutoTo 元件"""
    
    # 建立 agent
    agent = AgentLoop(config_mgr, memory_mgr)
    
    # 註冊台灣工具
    register_my_tool(agent)
    
    # 初始化 channels
    channels = {}
    if config_mgr.get("channels", {}).get("myChannel", {}).get("enabled"):
        channels["myChannel"] = MyChannel(config_mgr.get("channels")["myChannel"])
    
    return agent, channels
```

## 測試

### 單元測試

```python
# tests/test_my_tool.py

import pytest
from examples.tools.my_tool import MyTool

def test_my_tool_execute():
    """測試工具執行"""
    tool = MyTool()
    result = tool.execute(param1="test", param2=123)
    assert "test" in result
    assert "123" in result

def test_my_tool_name():
    """測試工具名稱"""
    tool = MyTool()
    assert tool.name == "my_tool"

def test_my_tool_description():
    """測試工具說明"""
    tool = MyTool()
    assert len(tool.description) > 0
```

### 整合測試

```python
# tests/test_integration.py

import pytest
from examples.app_setup import init_autoto_components

def test_agent_with_tool():
    """測試 agent 使用工具"""
    config_mgr = {
        "providers": {
            "openrouter": {"apiKey": "test_key"}
        }
    }
    
    agent, channels = init_autoto_components(config_mgr, None)
    
    # 測試工具是否註冊
    assert "my_tool" in agent.tools
```

### 執行測試

```bash
# 執行所有測試
pytest

# 執行特定測試
pytest tests/test_my_tool.py

# 顯示詳細輸出
pytest -v

# 顯示 print 輸出
pytest -s

# 測試覆蓋率
pytest --cov=backend
```

## 程式碼品質

### 格式化

```bash
# 格式化所有程式碼
black backend/

# 檢查但不修改
black --check backend/
```

### Linting

```bash
# 執行 flake8
flake8 backend/

# 忽略特定錯誤
flake8 --ignore=E501,W503 backend/
```

### 型別檢查

```bash
# 執行 mypy
mypy backend/
```

## 除錯技巧

### 1. 使用 Python Debugger

```python
# 在程式碼中加入斷點
import pdb; pdb.set_trace()

# 或使用 ipdb（更好用）
import ipdb; ipdb.set_trace()
```

### 2. 日誌記錄

```python
import logging

# 設定日誌
logging.basicConfig(
    level=logging.DEBUG,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)

logger = logging.getLogger(__name__)

# 使用日誌
logger.debug("除錯訊息")
logger.info("資訊訊息")
logger.warning("警告訊息")
logger.error("錯誤訊息")
```

### 3. 查看 AutoTo 後端狀態與日誌

```bash
# 檢查後端狀態
curl http://127.0.0.1:5678/api/status
```

## 效能優化

### 1. 使用快取

```python
from functools import lru_cache

class MyTool(BaseTool):
    
    @lru_cache(maxsize=128)
    def _fetch_data(self, key: str):
        """快取資料查詢結果"""
        # 耗時的操作
        return expensive_operation(key)
```

### 2. 非同步處理

```python
import asyncio

class MyTool(BaseTool):
    
    async def execute_async(self, **kwargs):
        """非同步執行"""
        result = await self._async_operation()
        return result
```

### 3. 批次處理

```python
def batch_process(items, batch_size=10):
    """批次處理項目"""
    for i in range(0, len(items), batch_size):
        batch = items[i:i + batch_size]
        yield process_batch(batch)
```

## 部署

### 開發環境

```bash
# 安裝依賴
python3 -m pip install -r backend/requirements.txt

# 啟動 AutoTo
./start.sh
```

### 生產環境（Docker）

```dockerfile
# Dockerfile
FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install -r requirements.txt

COPY . .

CMD ["python", "backend/server.py", "--host", "0.0.0.0", "--port", "5678"]
```

```bash
# 建立映像
docker build -t autoto .

# 執行容器
docker run -d \
  -v ~/.autoto:/root/.autoto \
  -p 5678:5678 \
  autoto
```

### 使用 systemd（Linux）

```ini
# /etc/systemd/system/autoto.service
[Unit]
Description=AutoTo Backend
After=network.target

[Service]
Type=simple
User=autoto
WorkingDirectory=/opt/autoto
ExecStart=/opt/autoto/venv/bin/python /opt/autoto/backend/server.py --host 0.0.0.0 --port 5678
Restart=always

[Install]
WantedBy=multi-user.target
```

```bash
# 啟動服務
sudo systemctl enable autoto
sudo systemctl start autoto
```

## 貢獻指南

### 提交 PR 前檢查清單

- [ ] 程式碼已格式化（black）
- [ ] 通過 linting（flake8）
- [ ] 通過型別檢查（mypy）
- [ ] 所有測試通過（pytest）
- [ ] 已加入新功能的測試
- [ ] 已更新文檔
- [ ] Commit 訊息清楚明確

### Commit 訊息格式

```
<type>(<scope>): <subject>

<body>

<footer>
```

類型：
- `feat`: 新功能
- `fix`: 修復 bug
- `docs`: 文檔更新
- `style`: 程式碼格式（不影響功能）
- `refactor`: 重構
- `test`: 測試相關
- `chore`: 建置或輔助工具

範例：
```
feat(tools): 加入台鐵時刻表查詢工具

實作台鐵時刻表 API 整合，支援：
- 車次查詢
- 時刻表查詢
- 票價查詢

Closes #123
```

## 常見問題

### Q: 如何除錯 agent 的工具呼叫？

```bash
# 先啟動 AutoTo，再查看狀態 / 日誌輸出
curl http://127.0.0.1:5678/api/status
```

### Q: 如何測試 webhook？

```bash
# 使用 ngrok
ngrok http 5678

# 或使用 curl 模擬 webhook
curl -X POST http://localhost:5678/webhook/line \
  -H "Content-Type: application/json" \
  -d '{"events": [...]}'
```

### Q: 如何更新 AutoTo？

```bash
cd autoto
git pull
bash install.sh
```

## 資源

- [README_TW.md](README_TW.md)
- [LINE Messaging API 文檔](https://developers.line.biz/en/docs/messaging-api/)
- [中央氣象署 API 文檔](https://opendata.cwa.gov.tw/dist/opendata-swagger.html)
- [Python 最佳實踐](https://docs.python-guide.org/)

## 聯絡

有問題或建議？歡迎開 Issue 或 PR！
