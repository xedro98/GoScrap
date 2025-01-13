import requests
import json
import logging

logging.basicConfig(level=logging.DEBUG)
logger = logging.getLogger(__name__)

def make_request():
    url = "http://localhost:8080/scrape"
    payload = {
        "query": "Software Engineer",
        "locations": ["New York"],
        "limit": 10
    }

    session = requests.Session()
    req = requests.Request('POST', url, json=payload)
    prepared_req = req.prepare()

    logger.info(f"Sending request to: {url}")
    logger.info(f"Request method: {prepared_req.method}")
    logger.info(f"Request headers: {prepared_req.headers}")
    logger.info(f"Request body: {prepared_req.body.decode('utf-8')}")

    try:
        response = session.send(prepared_req)
        logger.info(f"Status Code: {response.status_code}")
        logger.info(f"Response Headers: {dict(response.headers)}")
        logger.info(f"Response Body: {response.text[:200]}...")

        if response.status_code == 200:
            logger.info("Request successful")
        else:
            logger.error(f"Request failed with status code: {response.status_code}")

        return response.status_code, response.text

    except requests.RequestException as e:
        logger.error(f"Request failed: {e}")
        return None, str(e)

def main():
    logger.info("Starting the script")
    status_code, response_text = make_request()
    
    if status_code == 200:
        logger.info("Script completed successfully")
    else:
        logger.error("Script completed with errors")

    logger.info("Script finished")

if __name__ == "__main__":
    main()