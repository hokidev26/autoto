#!/usr/bin/env python3
"""Telegram Channel - 使用 asyncio 在背景線程運行"""

import asyncio
import socket
import threading
import traceback

# 強制 Python 優先使用 IPv4（解決某些網路環境 IPv6 連不到 Telegram 的問題）
_original_getaddrinfo = socket.getaddrinfo
def _ipv4_getaddrinfo(host, port, family=0, type=0, proto=0, flags=0):
    return _original_getaddrinfo(host, port, socket.AF_INET, type, proto, flags)

try:
    from telegram import Update
    from telegram.ext import Application, CommandHandler, MessageHandler, filters, ContextTypes
    from telegram.request import HTTPXRequest
    HAS_TELEGRAM = True
except ImportError:
    HAS_TELEGRAM = False


class TelegramChannel:
    def __init__(self, cfg, agent):
        if not HAS_TELEGRAM:
            raise ImportError('python-telegram-bot 未安裝，請執行: pip install python-telegram-bot')
        self.agent = agent
        self.token = cfg['botToken']
        self._running = False
        self._app = None

    def _build_app(self):
        return (
            Application.builder()
            .token(self.token)
            .request(HTTPXRequest(
                connect_timeout=30.0,
                read_timeout=30.0,
                write_timeout=30.0,
                pool_timeout=30.0,
            ))
            .get_updates_request(HTTPXRequest(
                connect_timeout=30.0,
                read_timeout=30.0,
                write_timeout=30.0,
                pool_timeout=30.0,
            ))
            .build()
        )

    def run(self):
        self._running = True
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)

        # 在此線程強制 IPv4
        socket.getaddrinfo = _ipv4_getaddrinfo

        self._app = self._build_app()

        async def start(update: Update, context: ContextTypes.DEFAULT_TYPE):
            await update.message.reply_text('👋 你好！我是 AutoTo，有什麼可以幫你的？')

        async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE):
            user_msg = update.message.text
            if not user_msg:
                return
            user_id = update.effective_user.id
            session_id = f'telegram-{user_id}'
            try:
                response = await asyncio.get_event_loop().run_in_executor(
                    None, self.agent.process_message, session_id, user_msg, 'telegram')
                if len(response) > 4000:
                    for i in range(0, len(response), 4000):
                        await update.message.reply_text(response[i:i+4000])
                else:
                    await update.message.reply_text(response)
            except Exception as e:
                print(f'  ❌ Telegram handle_message error: {e}')
                traceback.print_exc()
                await update.message.reply_text('⚠️ 處理訊息時發生錯誤，請稍後再試。')

        self._app.add_handler(CommandHandler('start', start))
        self._app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))

        print('  ✈️ Telegram Bot 連線中...')

        async def _run():
            max_retries = 5
            for attempt in range(max_retries):
                try:
                    await self._app.initialize()
                    await self._app.start()
                    await self._app.updater.start_polling(
                        drop_pending_updates=True,
                        allowed_updates=Update.ALL_TYPES,
                    )
                    print(f'  ✅ Telegram polling 已開始')
                    break
                except Exception as e:
                    print(f'  ⚠️ Telegram 連線失敗 (第 {attempt+1} 次): {e}')
                    if attempt < max_retries - 1:
                        wait = (attempt + 1) * 5
                        print(f'  ⏳ {wait} 秒後重試...')
                        try:
                            await self._app.shutdown()
                        except:
                            pass
                        self._app = self._build_app()
                        self._app.add_handler(CommandHandler('start', start))
                        self._app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))
                        await asyncio.sleep(wait)
                    else:
                        print(f'  ❌ Telegram 連線失敗，已達最大重試次數')
                        return

            while self._running:
                await asyncio.sleep(1)

            await self._app.updater.stop()
            await self._app.stop()
            await self._app.shutdown()

        loop.run_until_complete(_run())

    def stop(self):
        self._running = False
