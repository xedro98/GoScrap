import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  vus: 100,
  iterations: 1800,
};

export default function() {
  const url = 'http://localhost:8080/scrape';
  const payload = JSON.stringify({
    query: 'Software Engineer',
    locations: ['New York'],
    limit: 10
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
    },
  };

  const res = http.post(url, payload, params);

  check(res, {
    'status is 200': (r) => r.status === 200,
  });

  console.log(`Response status: ${res.status}`);
  console.log(`Response body: ${res.body.slice(0, 200)}...`); // Log only the first 200 characters

  sleep(1);
}