# 台灣特色工具設計文件

## 1. 台灣天氣工具

### 中央氣象署 API 整合

```python
# examples/tools/tw_weather.py

import requests
from typing import Dict, Optional, List
from datetime import datetime

class TaiwanWeatherTool:
    """
    中央氣象署開放資料 API 整合
    API 文件: https://opendata.cwa.gov.tw/
    """
    
    def __init__(self, api_key: str):
        self.api_key = api_key
        self.base_url = "https://opendata.cwa.gov.tw/api/v1/rest/datastore"
    
    def get_current_weather(self, city: str) -> Dict:
        """
        取得即時天氣
        
        Args:
            city: 城市名稱（台北市、新北市、桃園市等）
        
        Returns:
            天氣資訊字典
        """
        endpoint = f"{self.base_url}/O-A0003-001"
        params = {
            "Authorization": self.api_key,
            "locationName": city
        }
        
        try:
            response = requests.get(endpoint, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["success"] == "true":
                weather_data = data["records"]["location"][0]
                elements = {e["elementName"]: e["elementValue"] 
                           for e in weather_data["weatherElement"]}
                
                return {
                    "city": city,
                    "temp": elements.get("TEMP", "N/A"),
                    "humidity": elements.get("HUMD", "N/A"),
                    "weather": elements.get("WDSD", "N/A"),
                    "timestamp": weather_data["time"]["obsTime"]
                }
            else:
                return {"error": "無法取得天氣資料"}
                
        except Exception as e:
            return {"error": f"API 錯誤: {str(e)}"}
    
    def get_forecast(self, city: str, days: int = 7) -> List[Dict]:
        """
        取得未來天氣預報
        
        Args:
            city: 城市名稱
            days: 預報天數（1-7天）
        
        Returns:
            預報資料列表
        """
        endpoint = f"{self.base_url}/F-C0032-001"
        params = {
            "Authorization": self.api_key,
            "locationName": city
        }
        
        try:
            response = requests.get(endpoint, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["success"] == "true":
                location = data["records"]["location"][0]
                weather_elements = location["weatherElement"]
                
                # 解析預報資料
                forecasts = []
                time_periods = weather_elements[0]["time"]
                
                for period in time_periods[:days]:
                    forecast = {
                        "start_time": period["startTime"],
                        "end_time": period["endTime"],
                        "weather": self._get_element_value(weather_elements, "Wx", period),
                        "temp_min": self._get_element_value(weather_elements, "MinT", period),
                        "temp_max": self._get_element_value(weather_elements, "MaxT", period),
                        "pop": self._get_element_value(weather_elements, "PoP", period)  # 降雨機率
                    }
                    forecasts.append(forecast)
                
                return forecasts
            else:
                return [{"error": "無法取得預報資料"}]
                
        except Exception as e:
            return [{"error": f"API 錯誤: {str(e)}"}]
    
    def get_typhoon_info(self) -> Optional[Dict]:
        """取得颱風警報資訊"""
        endpoint = f"{self.base_url}/W-C0033-001"
        params = {"Authorization": self.api_key}
        
        try:
            response = requests.get(endpoint, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["success"] == "true" and data["records"]["record"]:
                typhoon = data["records"]["record"][0]
                return {
                    "name": typhoon.get("typhoonName"),
                    "warning_type": typhoon.get("warningType"),
                    "issue_time": typhoon.get("issueTime"),
                    "content": typhoon.get("content")
                }
            else:
                return None  # 無颱風警報
                
        except Exception as e:
            return {"error": f"API 錯誤: {str(e)}"}
    
    def get_earthquake_info(self) -> List[Dict]:
        """取得最近地震資訊"""
        endpoint = f"{self.base_url}/E-A0016-001"
        params = {"Authorization": self.api_key}
        
        try:
            response = requests.get(endpoint, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["success"] == "true":
                earthquakes = []
                for eq in data["records"]["Earthquake"][:5]:  # 最近5筆
                    earthquakes.append({
                        "time": eq["EarthquakeInfo"]["OriginTime"],
                        "magnitude": eq["EarthquakeInfo"]["EarthquakeMagnitude"]["MagnitudeValue"],
                        "depth": eq["EarthquakeInfo"]["FocalDepth"],
                        "location": eq["EarthquakeInfo"]["Epicenter"]["Location"],
                        "intensity": eq["Intensity"]["ShakingArea"][0]["AreaIntensity"] if eq["Intensity"]["ShakingArea"] else "N/A"
                    })
                return earthquakes
            else:
                return [{"error": "無法取得地震資料"}]
                
        except Exception as e:
            return [{"error": f"API 錯誤: {str(e)}"}]
    
    def get_air_quality(self, city: str) -> Dict:
        """
        取得空氣品質 (AQI)
        使用環保署 API
        """
        # 環保署 API
        url = "https://data.moenv.gov.tw/api/v2/aqx_p_432"
        params = {
            "api_key": self.api_key,  # 需要另外申請環保署 API key
            "filters": f"County,EQ,{city}"
        }
        
        try:
            response = requests.get(url, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["records"]:
                record = data["records"][0]
                return {
                    "city": city,
                    "site": record["SiteName"],
                    "aqi": record["AQI"],
                    "status": record["Status"],
                    "pm25": record["PM2.5"],
                    "pm10": record["PM10"],
                    "publish_time": record["PublishTime"]
                }
            else:
                return {"error": "無法取得空氣品質資料"}
                
        except Exception as e:
            return {"error": f"API 錯誤: {str(e)}"}
    
    def _get_element_value(self, elements: List, element_name: str, time_period: Dict) -> str:
        """輔助函數：從預報資料中提取特定元素值"""
        for element in elements:
            if element["elementName"] == element_name:
                for time in element["time"]:
                    if time["startTime"] == time_period["startTime"]:
                        return time["parameter"]["parameterName"]
        return "N/A"


# Agent Tool 註冊
def register_weather_tool(agent):
    """將天氣工具註冊到 AutoTo agent"""
    
    weather_tool = TaiwanWeatherTool(api_key="YOUR_CWA_API_KEY")
    
    @agent.tool(
        name="taiwan_weather",
        description="查詢台灣天氣資訊，包含即時天氣、預報、颱風、地震、空氣品質"
    )
    def taiwan_weather(
        action: str,  # "current", "forecast", "typhoon", "earthquake", "aqi"
        city: str = "臺北市",
        days: int = 3
    ) -> str:
        """
        查詢台灣天氣
        
        Args:
            action: 查詢類型
            city: 城市名稱
            days: 預報天數
        """
        if action == "current":
            result = weather_tool.get_current_weather(city)
            return f"{city}目前溫度 {result['temp']}°C，濕度 {result['humidity']}%"
        
        elif action == "forecast":
            forecasts = weather_tool.get_forecast(city, days)
            summary = f"{city}未來{days}天天氣預報：\n"
            for f in forecasts:
                summary += f"• {f['start_time'][:10]}: {f['weather']}, {f['temp_min']}-{f['temp_max']}°C, 降雨機率{f['pop']}%\n"
            return summary
        
        elif action == "typhoon":
            typhoon = weather_tool.get_typhoon_info()
            if typhoon:
                return f"颱風警報：{typhoon['name']} - {typhoon['warning_type']}\n{typhoon['content']}"
            else:
                return "目前無颱風警報"
        
        elif action == "earthquake":
            earthquakes = weather_tool.get_earthquake_info()
            summary = "最近地震資訊：\n"
            for eq in earthquakes:
                summary += f"• {eq['time']} 規模{eq['magnitude']} {eq['location']}\n"
            return summary
        
        elif action == "aqi":
            aqi = weather_tool.get_air_quality(city)
            return f"{city} {aqi['site']} 空氣品質：AQI {aqi['aqi']} ({aqi['status']}), PM2.5: {aqi['pm25']}"
        
        else:
            return "不支援的查詢類型"
```

## 2. 統一發票工具

```python
# autoto_tw/tools/tw_invoice.py

import requests
from typing import Dict, List
from datetime import datetime

class TaiwanInvoiceTool:
    """統一發票對獎工具"""
    
    def __init__(self):
        self.base_url = "https://invoice.etax.nat.gov.tw/invoice.xml"
    
    def get_winning_numbers(self, year_month: str = None) -> Dict:
        """
        取得中獎號碼
        
        Args:
            year_month: 民國年月，例如 "11301" (113年1-2月)
                       如果不提供，自動取得最新期
        """
        if not year_month:
            # 自動計算當期
            now = datetime.now()
            year = now.year - 1911  # 轉民國年
            month = now.month
            period = "01" if month <= 2 else \
                    "03" if month <= 4 else \
                    "05" if month <= 6 else \
                    "07" if month <= 8 else \
                    "09" if month <= 10 else "11"
            year_month = f"{year}{period}"
        
        try:
            response = requests.get(self.base_url, timeout=10)
            response.raise_for_status()
            
            # 解析 XML（簡化版，實際需要用 xml.etree）
            # 這裡假設已經解析完成
            return {
                "period": year_month,
                "special_prize": "12345678",  # 特別獎
                "grand_prize": "87654321",    # 特獎
                "first_prize": ["11111111", "22222222", "33333333"],  # 頭獎
                "additional": ["444", "555", "666"]  # 增開六獎
            }
            
        except Exception as e:
            return {"error": f"無法取得中獎號碼: {str(e)}"}
    
    def check_invoice(self, invoice_number: str, year_month: str = None) -> Dict:
        """
        對獎
        
        Args:
            invoice_number: 發票號碼（8碼）
            year_month: 期別
        
        Returns:
            中獎資訊
        """
        winning = self.get_winning_numbers(year_month)
        
        if "error" in winning:
            return winning
        
        # 檢查特別獎
        if invoice_number == winning["special_prize"]:
            return {"prize": "特別獎", "amount": 10000000}
        
        # 檢查特獎
        if invoice_number == winning["grand_prize"]:
            return {"prize": "特獎", "amount": 2000000}
        
        # 檢查頭獎
        for first in winning["first_prize"]:
            if invoice_number == first:
                return {"prize": "頭獎", "amount": 200000}
            elif invoice_number[-7:] == first[-7:]:
                return {"prize": "二獎", "amount": 40000}
            elif invoice_number[-6:] == first[-6:]:
                return {"prize": "三獎", "amount": 10000}
            elif invoice_number[-5:] == first[-5:]:
                return {"prize": "四獎", "amount": 4000}
            elif invoice_number[-4:] == first[-4:]:
                return {"prize": "五獎", "amount": 1000}
            elif invoice_number[-3:] == first[-3:]:
                return {"prize": "六獎", "amount": 200}
        
        # 檢查增開六獎
        for additional in winning["additional"]:
            if invoice_number[-3:] == additional:
                return {"prize": "增開六獎", "amount": 200}
        
        return {"prize": "未中獎", "amount": 0}


# Agent Tool 註冊
def register_invoice_tool(agent):
    """註冊發票工具"""
    
    invoice_tool = TaiwanInvoiceTool()
    
    @agent.tool(
        name="taiwan_invoice",
        description="統一發票對獎、查詢中獎號碼"
    )
    def taiwan_invoice(action: str, invoice_number: str = None) -> str:
        """
        統一發票功能
        
        Args:
            action: "winning_numbers" 或 "check"
            invoice_number: 發票號碼（對獎時需要）
        """
        if action == "winning_numbers":
            winning = invoice_tool.get_winning_numbers()
            return f"""本期中獎號碼：
特別獎：{winning['special_prize']}
特獎：{winning['grand_prize']}
頭獎：{', '.join(winning['first_prize'])}
增開六獎：{', '.join(winning['additional'])}
"""
        
        elif action == "check":
            if not invoice_number:
                return "請提供發票號碼"
            
            result = invoice_tool.check_invoice(invoice_number)
            if result["amount"] > 0:
                return f"🎉 恭喜中獎！{result['prize']} - 獎金 ${result['amount']:,} 元"
            else:
                return "很遺憾，未中獎。下次再接再厲！"
        
        else:
            return "不支援的操作"
```

## 3. 台股工具

```python
# autoto_tw/tools/tw_stock.py

import requests
from typing import Dict, List

class TaiwanStockTool:
    """台灣股市資訊工具"""
    
    def __init__(self):
        self.base_url = "https://www.twse.com.tw/exchangeReport"
    
    def get_stock_price(self, stock_id: str) -> Dict:
        """
        取得即時股價
        
        Args:
            stock_id: 股票代號（例如：2330）
        """
        url = f"{self.base_url}/STOCK_DAY"
        params = {
            "response": "json",
            "stockNo": stock_id
        }
        
        try:
            response = requests.get(url, params=params, timeout=10)
            response.raise_for_status()
            data = response.json()
            
            if data["stat"] == "OK":
                latest = data["data"][-1]  # 最新一天
                return {
                    "stock_id": stock_id,
                    "date": latest[0],
                    "open": latest[3],
                    "high": latest[4],
                    "low": latest[5],
                    "close": latest[6],
                    "volume": latest[1]
                }
            else:
                return {"error": "無法取得股價資料"}
                
        except Exception as e:
            return {"error": f"API 錯誤: {str(e)}"}
    
    def get_market_summary(self) -> Dict:
        """取得大盤資訊"""
        # 實作大盤指數查詢
        pass


# Agent Tool 註冊
def register_stock_tool(agent):
    """註冊股票工具"""
    
    stock_tool = TaiwanStockTool()
    
    @agent.tool(
        name="taiwan_stock",
        description="查詢台灣股市資訊"
    )
    def taiwan_stock(stock_id: str) -> str:
        """查詢股票"""
        result = stock_tool.get_stock_price(stock_id)
        if "error" in result:
            return result["error"]
        
        return f"""{stock_id} 股價資訊：
日期：{result['date']}
開盤：{result['open']}
最高：{result['high']}
最低：{result['low']}
收盤：{result['close']}
成交量：{result['volume']}
"""
```

## API Key 申請

### 1. 中央氣象署 API
- 網址：https://opendata.cwa.gov.tw/
- 註冊會員後即可取得 API Key
- 免費額度：每日 5,000 次

### 2. 環保署空氣品質 API
- 網址：https://data.moenv.gov.tw/
- 註冊後申請 API Key
- 免費使用

### 3. 統一發票 API
- 財政部提供，無需 API Key
- 直接存取公開資料

## 設定檔

```json
{
  "tools": {
    "taiwan": {
      "weather": {
        "enabled": true,
        "cwa_api_key": "YOUR_CWA_API_KEY",
        "moenv_api_key": "YOUR_MOENV_API_KEY"
      },
      "invoice": {
        "enabled": true,
        "auto_check": true,
        "notify_winning": true
      },
      "stock": {
        "enabled": true,
        "watchlist": ["2330", "2317", "2454"]
      }
    }
  }
}
```

## 使用範例

```python
# 在 AutoTo agent 中使用

# 查詢天氣
agent.run("台北市今天天氣如何？")
# → 呼叫 taiwan_weather(action="current", city="臺北市")

# 對發票
agent.run("幫我對發票 12345678")
# → 呼叫 taiwan_invoice(action="check", invoice_number="12345678")

# 查股票
agent.run("台積電股價多少？")
# → 呼叫 taiwan_stock(stock_id="2330")
```

## 下一步

- 加入更多台灣特色工具
- 整合台鐵/高鐵時刻表
- 整合 Ubike 即時資訊
- 整合便利商店取貨查詢
- 整合台灣電商 API
