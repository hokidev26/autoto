#!/usr/bin/env python3
"""Discord Channel"""

import asyncio

try:
    import discord
    from discord.ext import commands
    HAS_DISCORD = True
except ImportError:
    HAS_DISCORD = False


class DiscordChannel:
    def __init__(self, cfg, agent):
        if not HAS_DISCORD:
            raise ImportError('discord.py 未安裝，請執行: pip install discord.py')
        self.token = cfg['token']
        self.agent = agent
        self._running = False

        intents = discord.Intents.default()
        intents.message_content = True
        self.bot = commands.Bot(command_prefix='!', intents=intents)

        @self.bot.event
        async def on_ready():
            print(f'  🎮 Discord Bot 已連線: {self.bot.user}')

        @self.bot.event
        async def on_message(message):
            if message.author == self.bot.user:
                return
            await self.bot.process_commands(message)
            # 回應 @mention 或 DM
            if self.bot.user.mentioned_in(message) or isinstance(message.channel, discord.DMChannel):
                content = message.content.replace(f'<@{self.bot.user.id}>', '').strip()
                if content:
                    session_id = f'discord-{message.author.id}'
                    response = self.agent.process_message(session_id, content, source='discord')
                    # Discord 訊息長度限制 2000
                    for i in range(0, len(response), 1900):
                        await message.reply(response[i:i+1900])

    def run(self):
        self._running = True
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        loop.run_until_complete(self.bot.start(self.token))

    def stop(self):
        self._running = False
        asyncio.run_coroutine_threadsafe(self.bot.close(), self.bot.loop)
