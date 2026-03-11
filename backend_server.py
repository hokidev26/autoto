#!/usr/bin/env python3
"""
AutoTo 後端服務轉發入口
提供向後相容的啟動封裝，實際入口為 backend.server
"""

from backend.server import main


if __name__ == '__main__':
    main()
