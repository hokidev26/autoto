#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AutoTo Discord Bot Integration
讓 AutoTo 可以在 Discord 上使用
"""

import discord
from discord.ext import commands
import requests
import os
import asyncio

class AutoToDiscordBot:
    """AutoTo Discord Bot 類"""
    
    def __init__(self, token, autoto_api_url='http://127.0.0.1:5678'):
        """
        初始化 Discord Bot
        
        參數：
        - token: Discord Bot Token
        - autoto_api_url: AutoTo 後端 API 地址
        """
        self.token = token
        self.autoto_api_url = autoto_api_url
        
        # 設定 intents
        intents = discord.Intents.default()
        intents.message_content = True
        
        # 創建 Bot
        self.bot = commands.Bot(command_prefix='!', intents=intents)
        
        # 註冊事件
        @self.bot.event
        async def on_ready():
            print(f'✅ {self.bot.user} 已連線到 Discord!')
            print(f'📊 在 {len(self.bot.guilds)} 個伺服器中')
        
        @self.bot.event
        async def on_message(message):
            # 忽略自己的訊息
            if message.author == self.bot.user:
                return
            
            # 處理指令
            await self.bot.process_commands(message)
            
            # 如果訊息提到 Bot 或在 DM 中
            if self.bot.user.mentioned_in(message) or isinstance(message.channel, discord.DMChannel):
                await self.handle_message(message)
        
        # 註冊指令
        @self.bot.command(name='autoto', help='與 AutoTo 對話')
        async def autoto_command(ctx, *, message: str):
            """!autoto [訊息] - 與 AutoTo 對話"""
            await self.handle_command(ctx, message)
        
        @self.bot.command(name='system', help='查看系統資訊')
        async def system_command(ctx):
            """!system - 查看系統資訊"""
            await self.handle_command(ctx, '查看系統資訊')
        
        @self.bot.command(name='help_autoto', help='AutoTo 使用說明')
        async def help_command(ctx):
            """!help_autoto - 顯示使用說明"""
            help_text = """
🤖 **AutoTo Discord Bot 使用說明**

**基本用法：**
• 提及 @AutoTo [訊息] - 與 AutoTo 對話
• 私訊 AutoTo - 直接對話

**指令：**
• `!autoto [訊息]` - 與 AutoTo 對話
• `!system` - 查看系統資訊
• `!help_autoto` - 顯示此說明

**範例：**
• `!autoto 台北市今天天氣如何？`
• `!autoto 請 Kiro 寫一個 Python 程式`
• `!system`

**功能：**
✅ AI 對話
✅ 電腦控制
✅ 操作 Kiro
✅ 台灣本地化
            """
            await ctx.send(help_text)
    
    async def handle_message(self, message):
        """處理訊息"""
        # 移除提及
        content = message.content.replace(f'<@{self.bot.user.id}>', '').strip()
        
        if not content:
            await message.channel.send('你好！我是 AutoTo，有什麼可以幫你的嗎？')
            return
        
        # 顯示正在輸入
        async with message.channel.typing():
            # 調用 AutoTo API
            response_text = await self.call_autoto_api(content)
            
            # 分割長訊息（Discord 限制 2000 字元）
            if len(response_text) > 2000:
                chunks = [response_text[i:i+2000] for i in range(0, len(response_text), 2000)]
                for chunk in chunks:
                    await message.channel.send(chunk)
            else:
                await message.channel.send(response_text)
    
    async def handle_command(self, ctx, message):
        """處理指令"""
        async with ctx.typing():
            response_text = await self.call_autoto_api(message)
            
            # 分割長訊息
            if len(response_text) > 2000:
                chunks = [response_text[i:i+2000] for i in range(0, len(response_text), 2000)]
                for chunk in chunks:
                    await ctx.send(chunk)
            else:
                await ctx.send(response_text)
    
    async def call_autoto_api(self, message):
        """調用 AutoTo API"""
        try:
            # 使用 asyncio 執行同步請求
            loop = asyncio.get_event_loop()
            response = await loop.run_in_executor(
                None,
                lambda: requests.post(
                    f'{self.autoto_api_url}/api/chat',
                    json={'message': message},
                    timeout=30
                )
            )
            
            if response.status_code == 200:
                data = response.json()
                if data.get('success'):
                    return data.get('response', '抱歉，我無法回應')
                else:
                    return f"❌ 錯誤：{data.get('error', '未知錯誤')}"
            else:
                return f"❌ API 錯誤：{response.status_code}"
        except Exception as e:
            return f"❌ 連線錯誤：{str(e)}"
    
    def run(self):
        """啟動 Bot"""
        self.bot.run(self.token)


# ==================== 使用範例 ====================

def create_discord_bot():
    """創建 Discord Bot 實例"""
    
    # 從環境變數讀取設定
    token = os.getenv('DISCORD_BOT_TOKEN', '')
    
    if not token:
        print("❌ 請設定 DISCORD_BOT_TOKEN 環境變數")
        print("\n設定方法：")
        print("export DISCORD_BOT_TOKEN='你的 Bot Token'")
        return None
    
    # 創建 Bot
    bot = AutoToDiscordBot(token)
    return bot


if __name__ == '__main__':
    print("🤖 AutoTo Discord Bot")
    print("=" * 50)
    
    bot = create_discord_bot()
    
    if bot:
        print("\n✅ Discord Bot 正在啟動...")
        print("💡 使用 !help_autoto 查看使用說明")
        print("\n按 Ctrl+C 停止服務\n")
        
        try:
            bot.run()
        except KeyboardInterrupt:
            print("\n\n👋 Bot 已停止")
    else:
        print("\n❌ 啟動失敗，請檢查環境變數設定")
