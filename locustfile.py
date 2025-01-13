from locust import HttpLocust, TaskSet, task, between
import json

class UserBehavior(TaskSet):
    @task
    def scrape_test(self):
        payload = json.dumps({
            "query": "Software Engineer",
            "locations": ["New York"],
            "limit": 10
        })
        headers = {"Content-Type": "application/json"}
        with self.client.post("/scrape", data=payload, headers=headers, catch_response=True) as response:
            if response.status_code == 200:
                print(f"Success: {response.text[:100]}...")
            else:
                print(f"Failure: Status Code {response.status_code}, Response: {response.text[:100]}...")
                response.failure(f"Got status code {response.status_code}")

class WebsiteUser(HttpLocust):
    task_set = UserBehavior
    wait_time = between(3, 5)  # Increase wait time to reduce concurrency